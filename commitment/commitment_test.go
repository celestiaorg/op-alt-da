package commitment

import (
	"bytes"
	"testing"

	libshare "github.com/celestiaorg/go-square/v3/share"
)

// Helper function to create test namespace (29-byte user namespace)
func testNamespace() libshare.Namespace {
	// Namespaces are 29 bytes: 1 byte version + 28 bytes ID
	// User namespaces (version 0) require 18 leading zeros
	nsBytes := []byte{
		0x00, // version byte (user namespace)
		// 18 leading zeros (required for version 0)
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		// 10 bytes of actual ID
		0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a,
	}
	ns, err := libshare.NewNamespaceFromBytes(nsBytes)
	if err != nil {
		panic("failed to create test namespace: " + err.Error())
	}
	return ns
}

func testNamespace2() libshare.Namespace {
	nsBytes := []byte{
		0x00, // version byte
		// 18 leading zeros
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		// 10 bytes of different ID
		0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8, 0xf7, 0xf6,
	}
	ns, err := libshare.NewNamespaceFromBytes(nsBytes)
	if err != nil {
		panic("failed to create test namespace2: " + err.Error())
	}
	return ns
}

// TestComputeCommitment_Deterministic verifies that the same input produces the same commitment
func TestComputeCommitment_Deterministic(t *testing.T) {
	data := []byte("test data for deterministic commitment")
	namespace := testNamespace()

	// Compute commitment twice
	commitment1, err := ComputeCommitment(data, namespace)
	if err != nil {
		t.Fatalf("First commitment computation failed: %v", err)
	}

	commitment2, err := ComputeCommitment(data, namespace)
	if err != nil {
		t.Fatalf("Second commitment computation failed: %v", err)
	}

	// Should be identical
	if !bytes.Equal(commitment1, commitment2) {
		t.Errorf("Commitments not deterministic: %x != %x", commitment1, commitment2)
	}

	// Commitment should not be empty
	if len(commitment1) == 0 {
		t.Error("Commitment is empty")
	}
}

// TestComputeCommitment_DifferentData verifies different data produces different commitments
func TestComputeCommitment_DifferentData(t *testing.T) {
	namespace := testNamespace()

	data1 := []byte("first blob data")
	data2 := []byte("second blob data")

	commitment1, err := ComputeCommitment(data1, namespace)
	if err != nil {
		t.Fatalf("First commitment failed: %v", err)
	}

	commitment2, err := ComputeCommitment(data2, namespace)
	if err != nil {
		t.Fatalf("Second commitment failed: %v", err)
	}

	// Should be different
	if bytes.Equal(commitment1, commitment2) {
		t.Error("Different data produced same commitment")
	}
}

// TestComputeCommitment_DifferentNamespaces verifies different namespaces produce different commitments
func TestComputeCommitment_DifferentNamespaces(t *testing.T) {
	data := []byte("same data for both")

	namespace1 := testNamespace()
	namespace2 := testNamespace2()

	commitment1, err := ComputeCommitment(data, namespace1)
	if err != nil {
		t.Fatalf("First commitment failed: %v", err)
	}

	commitment2, err := ComputeCommitment(data, namespace2)
	if err != nil {
		t.Fatalf("Second commitment failed: %v", err)
	}

	// Should be different
	if bytes.Equal(commitment1, commitment2) {
		t.Error("Different namespaces produced same commitment")
	}
}

// TestComputeCommitment_VariousSizes tests commitment computation with various blob sizes
func TestComputeCommitment_VariousSizes(t *testing.T) {
	namespace := testNamespace()

	testCases := []struct {
		name string
		size int
	}{
		{"tiny", 1},
		{"small", 100},
		{"medium", 10000},
		{"large", 100000},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, tc.size)
			for i := range data {
				data[i] = byte(i % 256)
			}

			commitment, err := ComputeCommitment(data, namespace)
			if err != nil {
				t.Fatalf("Commitment failed for size %d: %v", tc.size, err)
			}

			if len(commitment) == 0 {
				t.Errorf("Empty commitment for size %d", tc.size)
			}

			t.Logf("Size %d bytes -> commitment length %d bytes", tc.size, len(commitment))
		})
	}
}

// TestComputeCommitment_EmptyData tests commitment computation with empty data
func TestComputeCommitment_EmptyData(t *testing.T) {
	namespace := testNamespace()

	data := []byte{}

	// Empty data should either work or return a clear error
	commitment, err := ComputeCommitment(data, namespace)

	// If it succeeds, commitment should not be empty
	if err == nil && len(commitment) == 0 {
		t.Error("Empty data produced empty commitment without error")
	}

	// Log the result for documentation
	if err != nil {
		t.Logf("Empty data returned error (expected): %v", err)
	} else {
		t.Logf("Empty data produced commitment of length %d", len(commitment))
	}
}

// TestComputeCommitment_SingleByte tests with minimal data
func TestComputeCommitment_SingleByte(t *testing.T) {
	namespace := testNamespace()

	data := []byte{0x42}

	commitment, err := ComputeCommitment(data, namespace)
	if err != nil {
		t.Fatalf("Single byte commitment failed: %v", err)
	}

	if len(commitment) == 0 {
		t.Error("Single byte produced empty commitment")
	}
}

// TestComputeCommitment_MaxSize tests with very large blob (close to 1MB)
func TestComputeCommitment_MaxSize(t *testing.T) {
	namespace := testNamespace()

	// Create ~1MB blob
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i % 256)
	}

	commitment, err := ComputeCommitment(data, namespace)
	if err != nil {
		t.Fatalf("Max size commitment failed: %v", err)
	}

	if len(commitment) == 0 {
		t.Error("Max size produced empty commitment")
	}

	t.Logf("1MB blob -> commitment length %d bytes", len(commitment))
}

// TestComputeCommitment_ConsistencyAcrossInvocations verifies consistency across multiple invocations
func TestComputeCommitment_ConsistencyAcrossInvocations(t *testing.T) {
	namespace := testNamespace()

	data := []byte("consistency test data")

	// Compute 10 times
	commitments := make([][]byte, 10)
	for i := 0; i < 10; i++ {
		c, err := ComputeCommitment(data, namespace)
		if err != nil {
			t.Fatalf("Commitment %d failed: %v", i, err)
		}
		commitments[i] = c
	}

	// All should be identical
	for i := 1; i < len(commitments); i++ {
		if !bytes.Equal(commitments[0], commitments[i]) {
			t.Errorf("Commitment %d differs from first: %x != %x", i, commitments[0], commitments[i])
		}
	}
}

// TestComputeCommitment_UniqueCommitments verifies many different blobs produce unique commitments
func TestComputeCommitment_UniqueCommitments(t *testing.T) {
	namespace := testNamespace()

	commitments := make(map[string]bool)

	// Create 100 different blobs and verify all produce unique commitments
	for i := 0; i < 100; i++ {
		data := []byte{}
		data = append(data, byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
		data = append(data, []byte(" - test data")...)

		commitment, err := ComputeCommitment(data, namespace)
		if err != nil {
			t.Fatalf("Commitment %d failed: %v", i, err)
		}

		commitmentStr := string(commitment)
		if commitments[commitmentStr] {
			t.Errorf("Duplicate commitment found for blob %d", i)
		}
		commitments[commitmentStr] = true
	}

	if len(commitments) != 100 {
		t.Errorf("Expected 100 unique commitments, got %d", len(commitments))
	}
}


