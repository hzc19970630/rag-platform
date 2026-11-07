package docreaderv1

import (
	"testing"

	"google.golang.org/protobuf/reflect/protodesc"
)

// The go_package option is embedded in the protobuf descriptor; hand-editing
// the generated file corrupts its length prefixes and panics on import.
func TestDescriptorMatchesProto(t *testing.T) {
	fd := File_contracts_proto_docreader_v1_docreader_proto
	if got := fd.Package(); got != "docreader.v1" {
		t.Fatalf("package = %q, want docreader.v1", got)
	}
	if got := protodesc.ToFileDescriptorProto(fd).GetOptions().GetGoPackage(); got != "github.com/hzc19970630/rag-platform/services/docreader/generated;docreaderv1" {
		t.Fatalf("go_package = %q", got)
	}
	service := fd.Services().ByName("DocumentReader")
	if service == nil || service.Methods().ByName("ParseDocument") == nil {
		t.Fatal("DocumentReader.ParseDocument is missing from the descriptor")
	}
}
