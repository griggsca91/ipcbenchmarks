package method

import (
	pb "ipc-bench/proto"

	"google.golang.org/protobuf/proto"
)

// marshalPayload wraps raw data in a pb.Payload and returns the protobuf-encoded bytes.
func marshalPayload(data []byte) ([]byte, error) {
	return proto.Marshal(&pb.Payload{Data: data})
}

// unmarshalPayload decodes protobuf bytes into a pb.Payload and returns the inner data.
func unmarshalPayload(data []byte) ([]byte, error) {
	var msg pb.Payload
	if err := proto.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// echoPayload unmarshals a pb.Payload and re-marshals it (simulates server-side processing).
func echoPayload(data []byte) ([]byte, error) {
	inner, err := unmarshalPayload(data)
	if err != nil {
		return nil, err
	}
	return marshalPayload(inner)
}

// protoWireSize returns the protobuf-encoded size for a Payload containing dataSize bytes.
func protoWireSize(dataSize int) int {
	return proto.Size(&pb.Payload{Data: make([]byte, dataSize)})
}
