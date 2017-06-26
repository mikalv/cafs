package remotesync

import (
	"bufio"
	"fmt"
	"github.com/indyjo/cafs"
	. "github.com/indyjo/cafs/ram"
	"io"
	"math/rand"
	"testing"
)

type writerPrinter struct {
	w io.Writer
}

func (p writerPrinter) Printf(format string, v ...interface{}) {
	fmt.Fprintf(p.w, format+"\n", v...)
}

func TestRemoteSync(t *testing.T) {
	// Re-use stores to test for leaks on the fly
	storeA := NewRamStorage(8 * 1024 * 1024)
	storeB := NewRamStorage(8 * 1024 * 1024)
	// LoggingEnabled = true

	// Test for different amounts of overlapping data
	for _, p := range []float64{0, 0.01, 0.05, 0.25, 0.5, 0.75, 0.95, 0.99, 1} {
		// Test for different number of blocks, so that storeB will _almost_ be filled up.
		// We can't test up to 512 because we don't know how much overhead data was produced
		// by the chunking algorithm (yes, RAM storage counts that overhead!)
		for _, nBlocks := range []int{0, 1, 2, 4, 8, 16, 32, 64, 128, 256, 400} {
			sigma := 0.25
			if nBlocks == 400 {
				sigma = 0
			}
			testWithParams(t, storeA, storeB, p, sigma, nBlocks)
		}
	}
}

func check(t *testing.T, msg string, err error) {
	if err != nil {
		t.Fatalf("Error %v: %v", msg, err)
	}
}

type flushWriter struct {
	w io.Writer
}

func (w flushWriter) Write(buf []byte) (int, error) {
	return w.w.Write(buf)
}

func (w flushWriter) Flush() {
}

func testWithParams(t *testing.T, storeA, storeB cafs.FileStorage, p, sigma float64, nBlocks int) {
	t.Logf("Testing with params: p=%f, nBlocks=%d", p, nBlocks)
	tempA := storeA.Create(fmt.Sprintf("Data A(%.2f,%d)", p, nBlocks))
	defer tempA.Dispose()
	tempB := storeB.Create(fmt.Sprintf("Data B(%.2f,%d)", p, nBlocks))
	defer tempB.Dispose()

	check(t, "creating similar data", createSimilarData(tempA, tempB, p, sigma, 8192, nBlocks))
	check(t, "closing tempA", tempA.Close())
	check(t, "closing tempB", tempB.Close())

	fileA := tempA.File()
	defer fileA.Dispose()

	builder := NewBuilder(storeB, 8, fmt.Sprintf("Recovered A(%.2f,%d)", p, nBlocks))
	defer builder.Dispose()

	// task: transfer file A to storage B
	// Pipe 1 is used to transfer the list of chunk hashes to the receiver
	pipeReader1, pipeWriter1 := io.Pipe()
	// Pipe 2 is used to transfer the bitmask of requested chunks back to the sender
	pipeReader2, pipeWriter2 := io.Pipe()
	// Pipe 3 is used to transfer the actual requested chunk data to the receiver
	pipeReader3, pipeWriter3 := io.Pipe()

	go func() {
		if err := WriteChunkHashes(fileA, pipeWriter1); err != nil {
			pipeWriter1.CloseWithError(fmt.Errorf("Error sending chunk hashes: %v", err))
		} else {
			pipeWriter1.Close()
		}
	}()
	go func() {
		if err := builder.WriteWishList(pipeReader1, flushWriter{pipeWriter2}); err != nil {
			pipeWriter2.CloseWithError(fmt.Errorf("Error generating wishlist: %v", err))
		} else {
			pipeWriter2.Close()
		}
	}()

	go func() {
		if err := WriteRequestedChunks(fileA, bufio.NewReader(pipeReader2), pipeWriter3, nil); err != nil {
			pipeWriter3.CloseWithError(fmt.Errorf("Error sending requested chunk data: %v", err))
		} else {
			pipeWriter3.Close()
		}
	}()

	var fileB cafs.File
	if f, err := builder.ReconstructFileFromRequestedChunks(pipeReader3); err != nil {
		t.Fatalf("Error reconstructing: %v", err)
	} else {
		fileB = f
		defer f.Dispose()
	}

	_ = fileB
	assertEqual(t, fileA.Open(), fileB.Open())
}

func assertEqual(t *testing.T, a, b io.ReadCloser) {
	bufA := make([]byte, 1)
	bufB := make([]byte, 1)
	for {
		nA, errA := a.Read(bufA)
		nB, errB := b.Read(bufB)
		if nA != nB {
			t.Fatal("Files differ in size")
		}
		if errA != errB {
			t.Fatalf("Error a:%v b:%v", errA, errB)
		}
		if bufA[0] != bufB[0] {
			t.Fatal("Files differ in content")
		}
		if errA == io.EOF && errB == io.EOF {
			break
		}
	}
	check(t, "closing file a in assertEqual", a.Close())
	check(t, "closing file b in assertEqual", b.Close())
}

func createSimilarData(tempA, tempB io.Writer, p, sigma, avgchunk float64, numchunks int) error {
	for numchunks > 0 {
		numchunks--
		lengthA := int(avgchunk*sigma*rand.NormFloat64() + avgchunk)
		if lengthA < 16 {
			lengthA = 16
		}
		data := randomBytes(lengthA)
		if _, err := tempA.Write(data); err != nil {
			return err
		}
		same := rand.Float64() <= p
		if same {
			if _, err := tempB.Write(data); err != nil {
				return err
			}
		} else {
			lengthB := int(avgchunk*sigma*rand.NormFloat64() + avgchunk)
			if lengthB < 16 {
				lengthB = 16
			}
			data = randomBytes(lengthB)
			if _, err := tempB.Write(data); err != nil {
				return err
			}
		}
	}
	return nil
}

func randomBytes(length int) []byte {
	result := make([]byte, 0, length)
	for len(result) < length {
		result = append(result, byte(rand.Int()))
	}
	return result
}
