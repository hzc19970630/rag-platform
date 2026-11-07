package docreader

import (
	"context"
	"fmt"

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
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
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
	}
	for _, item := range response.Blocks {
		result.Blocks = append(result.Blocks, ports.DocumentBlock{
			BlockID: item.BlockId, Kind: item.Kind, Text: item.Text, HeadingPath: item.HeadingPath,
			PageNumber: int(item.PageNumber), SourcePath: item.SourcePath,
			StartOffset: int(item.StartOffset), EndOffset: int(item.EndOffset),
		})
	}
	return result, nil
}
