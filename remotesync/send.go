//  BitWrk - A Bitcoin-friendly, anonymous marketplace for computing power
//  Copyright (C) 2013-2018 Jonas Eschenburg <jonas@bitwrk.net>
//
//  This program is free software: you can redistribute it and/or modify
//  it under the terms of the GNU General Public License as published by
//  the Free Software Foundation, either version 3 of the License, or
//  (at your option) any later version.
//
//  This program is distributed in the hope that it will be useful,
//  but WITHOUT ANY WARRANTY; without even the implied warranty of
//  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//  GNU General Public License for more details.
//
//  You should have received a copy of the GNU General Public License
//  along with this program.  If not, see <http://www.gnu.org/licenses/>.

package remotesync

import (
	"errors"
	"fmt"
	"github.com/indyjo/cafs"
	"github.com/indyjo/cafs/remotesync/shuffle"
	"io"
	"log"
)

// By passing a callback function to some of the transmissions functions,
// the caller may subscribe to the current transmission status.
type TransferStatusCallback func(bytesToTransfer, bytesTransferred int64)

// Writes a stream of chunk hash/length pairs into an io.Writer. Length is encoded
// as Varint. The original order of chunks is shuffled using permutation `perm`.
func WriteChunkHashes(file cafs.File, perm shuffle.Permutation, w io.Writer) error {
	if LoggingEnabled {
		log.Printf("Sender: Begin WriteChunkHashes")
		defer log.Printf("Sender: End WriteChunkHashes")
	}

	type chunk struct {
		key  cafs.SKey
		size int64
	}

	emptyChunk := chunk{
		key:  emptyKey,
		size: 0,
	}

	shuffler := shuffle.NewStreamShuffler(perm, emptyChunk, func(v interface{}) error {
		c := v.(chunk)
		if LoggingEnabled {
			log.Printf("Sender: Write %v", c.key)
		}
		if _, err := w.Write(c.key[:]); err != nil {
			return err
		}
		return writeVarint(w, c.size)
	})

	chunks := file.Chunks()
	defer chunks.Dispose()
	for chunks.Next() {
		if err := shuffler.Put(chunk{chunks.Key(), chunks.Size()}); err != nil {
			return err
		}
	}
	return shuffler.End()
}

// Iterates over a wishlist (read from `r` and pertaining to a permuted order of hashes),
// and calls `f` for each chunk of `file`, requested or not.
// If `f` returns an error, aborts the iteration and also returns the error.
func forEachChunk(storage cafs.FileStorage, file cafs.File, r io.ByteReader, perm shuffle.Permutation, f func(chunk cafs.File, requested bool) error) error {
	iter := file.Chunks()
	defer iter.Dispose()

	bits := newBitReader(r)

	// Prepare shuffler for iterating the file's chunks in shuffled order, matching them with
	// whishlist bits and calling `f` for each chunk, requested or not.
	shuffler := shuffle.NewStreamShuffler(perm, emptyKey, func(v interface{}) error {
		key := v.(cafs.SKey)
		if b, err := bits.ReadBit(); err != nil {
			return fmt.Errorf("Wishlist too short: %v on chunk %v", err, iter.Key())
		} else if key == emptyKey {
			// This is a placeholder key generated by the shuffler. Require that the receiver
			// signalled not to request the corresponding chunk.
			if b {
				return errors.New("Receiver requested the empty chunk")
			}
			return nil // Skip this key, it's not part of the file
		} else if chunk, err := storage.Get(&key); err != nil {
			return err
		} else {
			// Both the wishlist bit, and the corresponding chunk have been received correctly.
			// Now dispatch them to the delegate function.
			err := f(chunk, b)
			chunk.Dispose()
			if err != nil {
				return err
			}
		}
		return nil
	})

	// Iterate through the chunks of file and put their keys into the shuffler.
	for iter.Next() {
		if err := shuffler.Put(iter.Key()); err != nil {
			return err
		}
	}
	if err := shuffler.End(); err != nil {
		return err
	}

	// Expect whishlist byte stream to be read completely
	if _, err := r.ReadByte(); err != io.EOF {
		return errors.New("Wishlist too long")
	}
	return nil
}

// Writes a stream of chunk length / data pairs, permuted by a shuffler corresponding to `perm`,
// into an io.Writer, based on the chunks of a file and a matching permuted wishlist of requested chunks,
// read from `r`.
func WriteChunkData(storage cafs.FileStorage, file cafs.File, r io.ByteReader, perm shuffle.Permutation, w io.Writer, cb TransferStatusCallback) error {
	if LoggingEnabled {
		log.Printf("Sender: Begin WriteChunkData")
		defer log.Printf("Sender: End WriteChunkData")
	}

	// Determine the number of bytes to transmit by starting at the maximum and subtracting chunk
	// size whenever we read a 0 (chunk not requested)
	bytesToTransfer := file.Size()
	if cb != nil {
		cb(bytesToTransfer, 0)
	}

	// Iterate requested chunks. Write the chunk's length (as varint) and the chunk data
	// into the output writer. Update the number of bytes transferred on the go.
	var bytesTransferred int64
	return forEachChunk(storage, file, r, perm, func(chunk cafs.File, requested bool) error {
		if requested {
			if err := writeVarint(w, chunk.Size()); err != nil {
				return err
			}
			r := chunk.Open()
			defer r.Close()
			if n, err := io.Copy(w, r); err != nil {
				return err
			} else {
				bytesTransferred += n
			}
		} else {
			bytesToTransfer -= chunk.Size()
		}
		if cb != nil {
			// Notify callback of status
			cb(bytesToTransfer, bytesTransferred)
		}
		return nil
	})
}
