package docreader

import (
	"context"
	"errors"
	"fmt"
	"io"

	docreaderv1 "github.com/hzc19970630/rag-platform/contracts/proto/docreader/v1"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type Client struct {
	connection *grpc.ClientConn
	reader     docreaderv1.DocumentReaderClient
}

func New(address string) (*Client, error) {
	connection, err := grpc.NewClient(address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(128<<20),
			grpc.MaxCallSendMsgSize(8<<20),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("connect docreader: %w", err)
	}
	return &Client{connection: connection, reader: docreaderv1.NewDocumentReaderClient(connection)}, nil
}

func (c *Client) Close() error { return c.connection.Close() }

func (c *Client) Parse(ctx context.Context, source ports.SourceObject, content []byte) (ports.ParseResult, error) {
	response, err := c.reader.ParseDocument(ctx, &docreaderv1.ParseDocumentRequest{
		SchemaVersion:     ports.SchemaSourceObject,
		DocumentVersionId: source.DocumentVersionID,
		Filename:          source.Filename,
		MimeType:          source.MimeType,
		Content:           content,
	})
	if err != nil {
		return ports.ParseResult{}, fmt.Errorf("docreader parse: %w", err)
	}
	return resultFromResponse(response), nil
}

// ParseStream sends the source in bounded frames so neither side has to hold
// the whole file and a single gRPC message limit no longer caps file size.
func (c *Client) ParseStream(ctx context.Context, source ports.SourceObject, content io.Reader, sizeBytes, chunkBytes int64) (ports.ParseResult, error) {
	if chunkBytes <= 0 {
		chunkBytes = 2 << 20
	}
	stream, err := c.reader.ParseDocumentStream(ctx)
	if err != nil {
		return ports.ParseResult{}, fmt.Errorf("docreader stream: %w", err)
	}
	if err := stream.Send(&docreaderv1.ParseDocumentChunk{
		SchemaVersion:     ports.SchemaSourceObject,
		DocumentVersionId: source.DocumentVersionID,
		Filename:          source.Filename,
		MimeType:          source.MimeType,
	}); err != nil {
		return ports.ParseResult{}, fmt.Errorf("docreader stream metadata: %w", err)
	}
	buffer := make([]byte, chunkBytes)
	var sent int64
	for {
		read, readErr := content.Read(buffer)
		if read > 0 {
			if sizeBytes > 0 && sent+int64(read) > sizeBytes {
				return ports.ParseResult{}, fmt.Errorf("docreader stream exceeds declared size")
			}
			if err := stream.Send(&docreaderv1.ParseDocumentChunk{Content: buffer[:read]}); err != nil {
				// A server that rejects the request early (busy, too large)
				// closes the stream; surface its response instead of a bare EOF.
				if response, recvErr := stream.CloseAndRecv(); recvErr == nil && response != nil {
					return resultFromResponse(response), nil
				}
				return ports.ParseResult{}, fmt.Errorf("docreader stream content: %w", err)
			}
			sent += int64(read)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ports.ParseResult{}, fmt.Errorf("read source for docreader: %w", readErr)
		}
	}
	response, err := stream.CloseAndRecv()
	if err != nil {
		return ports.ParseResult{}, fmt.Errorf("docreader stream response: %w", err)
	}
	return resultFromResponse(response), nil
}

func resultFromResponse(response *docreaderv1.ParseDocumentResponse) ports.ParseResult {
	result := ports.ParseResult{
		SchemaVersion:     response.SchemaVersion,
		DocumentVersionID: response.DocumentVersionId,
		ContentHash:       response.ContentHash,
		MimeType:          response.MimeType,
		Title:             response.Title,
		Language:          response.Language,
		ParserVersion:     response.ParserVersion,
		ErrorCode:         response.ErrorCode,
		ErrorMessage:      response.ErrorMessage,
		Truncated:         response.Truncated,
		TotalRows:         response.TotalRows,
		IndexedRows:       response.IndexedRows,
		TotalSheets:       int(response.TotalSheets),
		IndexedSheets:     int(response.IndexedSheets),
	}
	for _, item := range response.Blocks {
		result.Blocks = append(result.Blocks, blockFromProto(item))
	}
	return result
}

func blockFromProto(item *docreaderv1.DocumentBlock) ports.DocumentBlock {
	result := ports.DocumentBlock{
		BlockID: item.BlockId, Kind: item.Kind, Text: item.Text, HeadingPath: item.HeadingPath,
		PageNumber: int(item.PageNumber), SourcePath: item.SourcePath,
		StartOffset: int(item.StartOffset), EndOffset: int(item.EndOffset),
	}
	if item.Table != nil {
		result.Table = &ports.TableMeta{
			Sheet: item.Table.Sheet, Header: item.Table.Header,
			RowStart: int(item.Table.RowStart), RowEnd: int(item.Table.RowEnd),
			ColumnCount: int(item.Table.ColumnCount),
		}
	}
	return result
}

// ParseBlocksStream uses the bidi RPC: blocks arrive in bounded events and the
// final summary carries stats/errors. The whole normalized document still lives
// in memory on the worker, but no single gRPC message contains all blocks.
func (c *Client) ParseBlocksStream(ctx context.Context, source ports.SourceObject, content io.Reader, sizeBytes, chunkBytes int64) (ports.ParseResult, error) {
	if chunkBytes <= 0 {
		chunkBytes = 2 << 20
	}
	stream, err := c.reader.ParseDocumentBlocks(ctx)
	if err != nil {
		return ports.ParseResult{}, fmt.Errorf("docreader block stream: %w", err)
	}
	if err := stream.Send(&docreaderv1.ParseDocumentChunk{
		SchemaVersion:     ports.SchemaSourceObject,
		DocumentVersionId: source.DocumentVersionID,
		Filename:          source.Filename,
		MimeType:          source.MimeType,
	}); err != nil {
		return ports.ParseResult{}, fmt.Errorf("docreader block stream metadata: %w", err)
	}
	buffer := make([]byte, chunkBytes)
	var sent int64
	for {
		read, readErr := content.Read(buffer)
		if read > 0 {
			if sizeBytes > 0 && sent+int64(read) > sizeBytes {
				return ports.ParseResult{}, fmt.Errorf("docreader block stream exceeds declared size")
			}
			if err := stream.Send(&docreaderv1.ParseDocumentChunk{Content: buffer[:read]}); err != nil {
				// A server that rejects the upload early closes the receive
				// side before yielding its summary. Drain the events so the
				// caller sees the parser error code instead of a bare EOF.
				result, drainErr := drainBlockStream(stream)
				if drainErr == nil {
					return result, nil
				}
				if !errors.Is(drainErr, io.EOF) {
					return ports.ParseResult{}, fmt.Errorf("docreader block stream response: %w", drainErr)
				}
				return ports.ParseResult{}, fmt.Errorf("docreader block stream content: %w", err)
			}
			sent += int64(read)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return ports.ParseResult{}, fmt.Errorf("read source for docreader: %w", readErr)
		}
	}
	if err := stream.CloseSend(); err != nil {
		return ports.ParseResult{}, fmt.Errorf("docreader block stream close: %w", err)
	}
	blocks := make([]ports.DocumentBlock, 0, 4096)
	var summary *docreaderv1.ParseDocumentResponse
	for {
		event, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return ports.ParseResult{}, fmt.Errorf("docreader block stream response: %w", recvErr)
		}
		for _, item := range event.Blocks {
			blocks = append(blocks, blockFromProto(item))
		}
		if event.Summary != nil {
			summary = event.Summary
		}
	}
	if summary == nil {
		return ports.ParseResult{}, fmt.Errorf("docreader block stream returned no summary")
	}
	result := resultFromResponse(summary)
	result.Blocks = append(blocks, result.Blocks...)
	return result, nil
}

// drainBlockStream collects events after the upload side closes early. The
// summary carries the parser error code; io.EOF means the server closed
// without one.
func drainBlockStream(stream docreaderv1.DocumentReader_ParseDocumentBlocksClient) (ports.ParseResult, error) {
	_ = stream.CloseSend()
	blocks := make([]ports.DocumentBlock, 0, 64)
	var summary *docreaderv1.ParseDocumentResponse
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			if summary == nil {
				return ports.ParseResult{}, err
			}
			break
		}
		for _, item := range event.Blocks {
			blocks = append(blocks, blockFromProto(item))
		}
		if event.Summary != nil {
			summary = event.Summary
		}
	}
	if summary == nil {
		return ports.ParseResult{}, io.EOF
	}
	result := resultFromResponse(summary)
	result.Blocks = append(blocks, result.Blocks...)
	return result, nil
}
