//go:build e2e
// +build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/celestiaorg/celestia-node/api/rpc/client"
	"github.com/celestiaorg/celestia-node/blob"
	libshare "github.com/celestiaorg/go-square/v3/share"
	"github.com/celestiaorg/tastora/framework/docker"
	"github.com/celestiaorg/tastora/framework/docker/dataavailability"
	"github.com/celestiaorg/tastora/framework/types"
	"github.com/stretchr/testify/require"
)

func TestE2E_PostDataToBridgeNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Setup Docker client and network
	dockerClient, dockerNetworkID := docker.Setup(t)
	defer docker.Cleanup(t, dockerClient)

	// Create a DA network builder
	builder := dataavailability.NewNetworkBuilder(t).
		WithDockerClient(dockerClient).
		WithDockerNetworkID(dockerNetworkID).
		WithChainID("e2e-test-chain")

	// Add a bridge node to the network
	bridgeNodeConfig := dataavailability.NewNodeBuilder().
		WithNodeType(types.BridgeNode).
		Build()
	builder = builder.WithNode(bridgeNodeConfig)

	// Build the network
	daNetwork, err := builder.Build(ctx)
	require.NoError(t, err, "Failed to build DA network")
	t.Log("DA network built successfully")

	// Start the bridge node
	bridgeNodes := daNetwork.GetBridgeNodes()
	require.NotEmpty(t, bridgeNodes, "No bridge nodes found in network")

	bridgeNode := bridgeNodes[0]
	t.Logf("Starting bridge node: %s", bridgeNode.Name())

	err = bridgeNode.Start(ctx)
	require.NoError(t, err, "Failed to start bridge node")
	t.Log("Bridge node started successfully")

	// Get network info from the bridge node
	networkInfo, err := bridgeNode.GetNetworkInfo(ctx)
	require.NoError(t, err, "Failed to get network info")

	// Get the RPC endpoint for the bridge node
	rpcAddr := networkInfo.External.RPCAddress()
	t.Logf("Bridge node RPC address: %s", rpcAddr)

	// Get auth token
	authToken, err := bridgeNode.GetAuthToken()
	require.NoError(t, err, "Failed to get auth token")

	// Create a celestia RPC client
	celestiaClient, err := client.NewClient(ctx, fmt.Sprintf("http://%s", rpcAddr), authToken)
	require.NoError(t, err, "Failed to create celestia client")
	defer celestiaClient.Close()

	// Create a test namespace
	namespace, err := libshare.NewNamespaceFromBytes([]byte("test-namespace"))
	require.NoError(t, err, "Failed to create namespace")
	t.Logf("Using namespace: %s", hex.EncodeToString(namespace.Bytes()))

	// Generate test data
	testDataSize := 1024 // 1KB
	testData := make([]byte, testDataSize)
	rand.New(rand.NewSource(time.Now().UnixNano())).Read(testData)

	// Add metadata to the test data
	testID := fmt.Sprintf("e2e-test-%d", time.Now().UnixNano())
	testDataWithMetadata := fmt.Sprintf("TestID: %s\nTimestamp: %s\nData: %s",
		testID, time.Now().Format(time.RFC3339), hex.EncodeToString(testData[:32]))
	testData = []byte(testDataWithMetadata)

	t.Logf("Generated test data (size: %d bytes)", len(testData))

	// Create a blob to submit
	testBlob, err := blob.NewBlob(libshare.ShareVersionZero, namespace, testData, nil)
	require.NoError(t, err, "Failed to create blob")

	commitment := testBlob.Commitment
	t.Logf("Blob commitment: %s", hex.EncodeToString(commitment))

	// Submit the blob to the bridge node using the celestia client
	height, err := celestiaClient.Blob.Submit(ctx, []*blob.Blob{testBlob}, nil)
	require.NoError(t, err, "Failed to submit blob to bridge node")
	t.Logf("Blob submitted successfully at height: %d", height)

	// Wait a bit for the blob to be included
	time.Sleep(2 * time.Second)

	// Verify we can retrieve the blob
	retrievedBlob, err := celestiaClient.Blob.Get(ctx, height, namespace, commitment)
	require.NoError(t, err, "Failed to retrieve blob from bridge node")
	require.NotNil(t, retrievedBlob, "Retrieved blob is nil")

	// Verify the data matches
	retrievedData := retrievedBlob.Data()
	require.True(t, bytes.Equal(testData, retrievedData),
		"Retrieved data does not match original data")

	t.Logf("Successfully retrieved and verified blob data (size: %d bytes)", len(retrievedData))

	// Verify the commitment matches
	retrievedCommitment := retrievedBlob.Commitment
	require.True(t, bytes.Equal(commitment, retrievedCommitment),
		"Retrieved commitment does not match original commitment")

	t.Log("E2E test completed successfully: Data posted and retrieved from bridge node")
}

func TestE2E_PostMultipleBlobsToBridgeNode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Setup Docker client and network
	dockerClient, dockerNetworkID := docker.Setup(t)
	defer docker.Cleanup(t, dockerClient)

	// Create a DA network builder
	builder := dataavailability.NewNetworkBuilder(t).
		WithDockerClient(dockerClient).
		WithDockerNetworkID(dockerNetworkID).
		WithChainID("e2e-test-chain-multi")

	// Add a bridge node
	bridgeNodeConfig := dataavailability.NewNodeBuilder().
		WithNodeType(types.BridgeNode).
		Build()
	builder = builder.WithNode(bridgeNodeConfig)

	// Build the network
	daNetwork, err := builder.Build(ctx)
	require.NoError(t, err, "Failed to build DA network")

	// Start the bridge node
	bridgeNodes := daNetwork.GetBridgeNodes()
	require.NotEmpty(t, bridgeNodes, "No bridge nodes found")
	bridgeNode := bridgeNodes[0]

	err = bridgeNode.Start(ctx)
	require.NoError(t, err, "Failed to start bridge node")

	// Get network info and create client
	networkInfo, err := bridgeNode.GetNetworkInfo(ctx)
	require.NoError(t, err, "Failed to get network info")

	authToken, err := bridgeNode.GetAuthToken()
	require.NoError(t, err, "Failed to get auth token")

	celestiaClient, err := client.NewClient(ctx, fmt.Sprintf("http://%s", networkInfo.External.RPCAddress()), authToken)
	require.NoError(t, err, "Failed to create celestia client")
	defer celestiaClient.Close()

	// Create namespace
	namespace, err := libshare.NewNamespaceFromBytes([]byte("test-namespace-multi"))
	require.NoError(t, err, "Failed to create namespace")

	// Submit multiple blobs
	numBlobs := 5
	blobs := make([]*blob.Blob, numBlobs)
	commitments := make([][]byte, numBlobs)

	for i := 0; i < numBlobs; i++ {
		testData := []byte(fmt.Sprintf("Test blob %d - timestamp: %s", i, time.Now().Format(time.RFC3339Nano)))
		testBlob, err := blob.NewBlob(libshare.ShareVersionZero, namespace, testData, nil)
		require.NoError(t, err, "Failed to create blob %d", i)

		blobs[i] = testBlob
		commitments[i] = testBlob.Commitment
	}

	// Submit all blobs
	height, err := celestiaClient.Blob.Submit(ctx, blobs, nil)
	require.NoError(t, err, "Failed to submit multiple blobs")
	t.Logf("Submitted %d blobs at height: %d", numBlobs, height)

	// Wait for inclusion
	time.Sleep(2 * time.Second)

	// Verify all blobs can be retrieved
	for i, commitment := range commitments {
		retrievedBlob, err := celestiaClient.Blob.Get(ctx, height, namespace, commitment)
		require.NoError(t, err, "Failed to retrieve blob %d", i)
		require.NotNil(t, retrievedBlob, "Retrieved blob %d is nil", i)

		retrievedData := retrievedBlob.Data()
		require.Contains(t, string(retrievedData), fmt.Sprintf("Test blob %d", i),
			"Retrieved data for blob %d does not match", i)

		t.Logf("Successfully verified blob %d", i)
	}

	t.Log("E2E test completed successfully: Multiple blobs posted and retrieved")
}
