/*
 * NETCAP - Network Capture Toolkit
 * Copyright (c) 2017 Philipp Mieden <dreadl0ck [at] protonmail [dot] ch>
 *
 * THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
 * WITH REGARD TO THIS SOFTWARE INCLUDING ALL IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR
 * ANY SPECIAL, DIRECT, INDIRECT, OR CONSEQUENTIAL DAMAGES OR ANY DAMAGES
 * WHATSOEVER RESULTING FROM LOSS OF USE, DATA OR PROFITS, WHETHER IN AN
 * ACTION OF CONTRACT, NEGLIGENCE OR OTHER TORTIOUS ACTION, ARISING OUT OF
 * OR IN CONNECTION WITH THE USE OR PERFORMANCE OF THIS SOFTWARE.
 */

package encoder

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dreadl0ck/netcap"

	"github.com/dreadl0ck/netcap/types"
	"github.com/dreadl0ck/netcap/utils"
	"github.com/golang/protobuf/proto"
	"github.com/google/gopacket"
	"github.com/google/kythe/kythe/go/platform/delimited"
)

var (
	// LayerEncoders map contains initialized encoders at runtime
	LayerEncoders = map[gopacket.LayerType]*LayerEncoder{}

	// contains all available encoders
	layerEncoderSlice = []*LayerEncoder{
		TCPEncoder,
		UDPEncoder,
		IPv4Encoder,
		IPv6Encoder,
		DHCPv4Encoder,
		DHCPv6Encoder,
		ICMPv4Encoder,
		ICMPv6Encoder,
		ICMPv6EchoEncoder,
		ICMPv6NeighborSolicitationEncoder,
		ICMPv6RouterSolicitationEncoder,
		DNSEncoder,
		ARPEncoder,
		EthernetEncoder,
		Dot1QEncoder,
		Dot11Encoder,
		NTPEncoder,
		SIPEncoder,
		IGMPEncoder,
		LLCEncoder,
		IPv6HopByHopEncoder,
		SCTPEncoder,
		SNAPEncoder,
		LinkLayerDiscoveryEncoder,
		ICMPv6NeighborAdvertisementEncoder,
		ICMPv6RouterAdvertisementEncoder,
		EthernetCTPEncoder,
		EthernetCTPReplyEncoder,
		LinkLayerDiscoveryInfoEncoder,
	}
)

type (
	// LayerEncoderHandler is the handler function for a layer encoder
	LayerEncoderHandler = func(layer gopacket.Layer, timestamp string) proto.Message

	// LayerEncoder represents a encoder for the gopacket.Layer type
	LayerEncoder struct {

		// Public
		Layer gopacket.LayerType
		Type  types.Type

		// Private
		file      *os.File
		bWriter   *bufio.Writer
		gWriter   *gzip.Writer
		dWriter   *delimited.Writer
		aWriter   *AtomicDelimitedWriter
		Handler   LayerEncoderHandler
		cWriter   *chanWriter
		csvWriter *csvWriter

		// Config
		compress bool
		csv      bool
		buffer   bool
		out      string
	}
)

/*
 *	Initialization
 */

// package level init
func init() {

	// get system block size for use as the buffer size of the buffered Writers
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		panic(err)
	}

	BlockSize = int(stat.Bsize)
}

// InitLayerEncoders initializes all layer encoders
func InitLayerEncoders(c Config) {

	var (
		in        = strings.Split(c.IncludeEncoders, ",")
		ex        = strings.Split(c.ExcludeEncoders, ",")
		inMap     = make(map[string]bool)
		selection []*LayerEncoder
	)

	if len(in) > 0 && in[0] != "" {

		for _, name := range in {
			if name != "" {
				// check if proto exists
				if _, ok := allEncoderNames[name]; !ok {
					invalidProto(name)
				}
				inMap[name] = true
			}
		}

		for _, e := range layerEncoderSlice {
			if _, ok := inMap[e.Layer.String()]; ok {
				selection = append(selection, e)
			}
		}
		layerEncoderSlice = selection
	}

	for _, name := range ex {
		if name != "" {
			// check if proto exists
			if _, ok := allEncoderNames[name]; !ok {
				invalidProto(name)
			}
			for i, e := range layerEncoderSlice {
				if name == e.Layer.String() {
					// remove encoder
					layerEncoderSlice = append(layerEncoderSlice[:i], layerEncoderSlice[i+1:]...)
					break
				}
			}
		}
	}

	for _, e := range layerEncoderSlice {

		// fmt.Println("init", d.layer)
		e.Init(c.Buffer, c.Compression, c.CSV, c.Out, c.WriteChan)

		// write header
		if e.csv {
			_, err := e.csvWriter.WriteHeader(netcap.InitRecord(e.Type))
			if err != nil {
				panic(err)
			}
		} else {
			err := e.aWriter.PutProto(NewHeader(e.Type, c))
			if err != nil {
				fmt.Println("failed to write header")
				panic(err)
			}
		}

		LayerEncoders[e.Layer] = e
	}
	fmt.Println("initialized", len(LayerEncoders), "layer encoders | buffer size:", BlockSize)
}

/*
 *	LayerEncoder Public
 */

// CreateLayerEncoder returns a new LayerEncoder instance
func CreateLayerEncoder(nt types.Type, lt gopacket.LayerType, handler LayerEncoderHandler) *LayerEncoder {
	return &LayerEncoder{
		Layer:   lt,
		Handler: handler,
		Type:    nt,
	}
}

// Encode is called for each layer
// this calls the handler function of the encoder
// and writes the serialized protobuf into the data pipe
func (d *LayerEncoder) Encode(l gopacket.Layer, timestamp time.Time) error {

	// fmt.Println("decode", d.Layer.String())

	decoded := d.Handler(l, utils.TimeToString(timestamp))
	if decoded != nil {
		if d.csv {
			_, err := d.csvWriter.WriteRecord(decoded)
			if err != nil {
				return err
			}
		} else {
			err := d.aWriter.PutProto(decoded)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// Init initializes and configures the encoder
func (d *LayerEncoder) Init(buffer, compress, csv bool, out string, writeChan bool) {

	d.compress = compress
	d.buffer = buffer
	d.csv = csv
	d.out = out

	if csv {

		// create file
		if compress {
			d.file = CreateFile(filepath.Join(out, d.Layer.String()), ".csv.gz")
		} else {
			d.file = CreateFile(filepath.Join(out, d.Layer.String()), ".csv")
		}

		if buffer {

			d.bWriter = bufio.NewWriterSize(d.file, BlockSize)

			if compress {
				d.gWriter = gzip.NewWriter(d.bWriter)
				d.csvWriter = NewCSVWriter(d.gWriter)
			} else {
				d.csvWriter = NewCSVWriter(d.bWriter)
			}
		} else {
			if compress {
				d.gWriter = gzip.NewWriter(d.file)
				d.csvWriter = NewCSVWriter(d.gWriter)
			} else {
				d.csvWriter = NewCSVWriter(d.file)
			}
		}
		return
	}

	if writeChan && buffer || writeChan && compress {
		panic("buffering or compression cannot be activated when running using writeChan")
	}

	// write into channel OR into file
	if writeChan {
		d.cWriter = newChanWriter()
	} else {
		if compress {
			d.file = CreateFile(filepath.Join(out, d.Layer.String()), ".ncap.gz")
		} else {
			d.file = CreateFile(filepath.Join(out, d.Layer.String()), ".ncap")
		}
	}

	// buffer data?
	// when using writeChan buffering is not possible
	if buffer {

		d.bWriter = bufio.NewWriterSize(d.file, BlockSize)
		if compress {
			d.gWriter = gzip.NewWriter(d.bWriter)
			d.dWriter = delimited.NewWriter(d.gWriter)
		} else {
			d.dWriter = delimited.NewWriter(d.bWriter)
		}
	} else {
		if compress {
			d.gWriter = gzip.NewWriter(d.file)
			d.dWriter = delimited.NewWriter(d.gWriter)
		} else {
			if writeChan {
				// write into channel writer without compression
				d.dWriter = delimited.NewWriter(d.cWriter)
			} else {
				d.dWriter = delimited.NewWriter(d.file)
			}
		}
	}

	d.aWriter = NewAtomicDelimitedWriter(d.dWriter)
}

// GetChan returns a channel to receive serialized protobuf data from
func (d *LayerEncoder) GetChan() <-chan []byte {
	return d.cWriter.Chan()
}

// Destroy closes and flushes all writers
func (d *LayerEncoder) Destroy() (name string, size int64) {
	if d.compress {
		CloseGzipWriters(d.gWriter)
	}
	if d.buffer {
		FlushWriters(d.bWriter)
	}
	return CloseFile(d.out, d.file, d.Layer.String())
}