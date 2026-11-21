package docreader

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	docreaderv1 "github.com/hzc19970630/rag-platform/contracts/proto/docreader/v1"
	"github.com/hzc19970630/rag-platform/internal/contracts/evidence"
	"github.com/hzc19970630/rag-platform/internal/contracts/ports"
	"google.golang.org/grpc"
)

type earlyRejectServer struct {
	docreaderv1.UnimplementedDocumentReaderServer
}

// ParseDocumentBlocks stops reading after the metadata frame so a large upload
// fails mid-send, mirroring docreader rejecting file_too_large.
func (earlyRejectServer) ParseDocumentBlocks(stream docreaderv1.DocumentReader_ParseDocumentBlocksServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	return stream.Send(&docreaderv1.ParseDocumentEvent{Summary: &docreaderv1.ParseDocumentResponse{
		SchemaVersion: "NormalizedDocumentV1",
		ErrorCode:     "file_too_large",
		ErrorMessage:  "file exceeds maximum size",
	}})
}

func TestParseBlocksStreamSurfacesEarlyRejection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	docreaderv1.RegisterDocumentReaderServer(server, earlyRejectServer{})
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	client, err := New(listener.Addr().String())
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	content := make([]byte, 32<<20)
	result, err := client.ParseBlocksStream(ctx, ports.SourceObject{
		Envelope: evidence.Envelope{DocumentVersionID: "ver-1"},
		Filename: "big.xlsx",
		MimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	}, bytes.NewReader(content), int64(len(content)), 2<<20)
	if err != nil {
		t.Fatalf("ParseBlocksStream returned %v, want parser result", err)
	}
	if result.ErrorCode != "file_too_large" {
		t.Fatalf("ErrorCode = %q, want file_too_large", result.ErrorCode)
	}
}
