package protobuf

import (
	"testing"

	"github.com/zourzouvillys/laredo/snapshotter/formattest"
)

func TestProtobuf_Conformance(t *testing.T) {
	formattest.Run(t, New())
}
