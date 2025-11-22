package batch

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/celestiaorg/op-alt-da/db"
)

// PackBlobs packs multiple blobs into a single batch blob
// Format: [count (4 bytes)] [size1 (4 bytes)] [data1] [size2] [data2] ...
func PackBlobs(blobs []*db.Blob, cfg *Config) ([]byte, error) {
	if len(blobs) == 0 {
		return nil, fmt.Errorf("cannot pack empty blob list")
	}

	if len(blobs) > cfg.MaxBlobs {
		return nil, fmt.Errorf("too many blobs: %d > %d", len(blobs), cfg.MaxBlobs)
	}

	buf := new(bytes.Buffer)

	// Write blob count
	if err := binary.Write(buf, binary.BigEndian, uint32(len(blobs))); err != nil {
		return nil, fmt.Errorf("write blob count: %w", err)
	}

	// Write each blob
	for i, blob := range blobs {
		// Write blob size
		if err := binary.Write(buf, binary.BigEndian, uint32(len(blob.Data))); err != nil {
			return nil, fmt.Errorf("write blob %d size: %w", i, err)
		}

		// Write blob data
		if _, err := buf.Write(blob.Data); err != nil {
			return nil, fmt.Errorf("write blob %d data: %w", i, err)
		}
	}

	data := buf.Bytes()
	if len(data) > cfg.MaxBatchSizeBytes {
		return nil, fmt.Errorf("batch too large: %d > %d bytes", len(data), cfg.MaxBatchSizeBytes)
	}

	return data, nil
}

// UnpackBlobs unpacks a batch blob into individual blobs
func UnpackBlobs(packedData []byte, cfg *Config) ([][]byte, error) {
	buf := bytes.NewReader(packedData)

	// Read blob count
	var count uint32
	if err := binary.Read(buf, binary.BigEndian, &count); err != nil {
		return nil, fmt.Errorf("read blob count: %w", err)
	}

	if count == 0 || count > uint32(cfg.MaxBlobs) {
		return nil, fmt.Errorf("invalid blob count: %d", count)
	}

	blobs := make([][]byte, count)

	// Read each blob
	for i := uint32(0); i < count; i++ {
		// Read blob size
		var size uint32
		if err := binary.Read(buf, binary.BigEndian, &size); err != nil {
			return nil, fmt.Errorf("read blob %d size: %w", i, err)
		}

		if size == 0 || size > uint32(cfg.MaxBatchSizeBytes) {
			return nil, fmt.Errorf("invalid blob %d size: %d", i, size)
		}

		// Read blob data
		blobData := make([]byte, size)
		if _, err := io.ReadFull(buf, blobData); err != nil {
			return nil, fmt.Errorf("read blob %d data: %w", i, err)
		}

		blobs[i] = blobData
	}

	return blobs, nil
}
