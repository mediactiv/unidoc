/*
 * This file is subject to the terms and conditions defined in
 * file 'LICENSE.txt', which is part of this source code package.
 */

package pdf

import (
	"bytes"
	"compress/zlib"
	"encoding/hex"
	"fmt"
)

// Decodes the stream.
// Supports FlateDecode, ASCIIHexDecode.
func (this *PdfParser) decodeStream(obj *PdfObjectStream) ([]byte, error) {
	log.Debug("Decode stream")

	log.Debug("filter %s", (*obj).PdfObjectDictionary)
	method := (*(obj.PdfObjectDictionary))["Filter"].(*PdfObjectName)
	if *method == "FlateDecode" {
		// Refactor to a separate function.
		// Revamp this support to handle TIFF predictor (2).
		// Also handle more filter bytes and check
		// BitsPerComponent.  Default value is 8, currently we are only
		// supporting that one.
		predictor := 1

		decodeParams, hasDecodeParams := (*(obj.PdfObjectDictionary))["DecodeParms"].(*PdfObjectDictionary)
		if hasDecodeParams {
			log.Debug("decode params: %s", decodeParams.String())
			predictor = int(*((*decodeParams)["Predictor"].(*PdfObjectInteger)))

			obits, hasbits := (*decodeParams)["BitsPerComponent"]
			if hasbits {
				pbits, ok := obits.(*PdfObjectInteger)
				if !ok {
					log.Error("Invalid BitsPerComponent")
					return nil, fmt.Errorf("Invalid BitsPerComponent")
				}
				if *pbits != 8 {
					return nil, fmt.Errorf("Currently only 8 bits for flatedecode supported")
				}
			}
		}
		log.Debug("Predictor: %d", predictor)

		log.Debug("Encoding method: %s", method)

		bufReader := bytes.NewReader(obj.Stream)
		r, err := zlib.NewReader(bufReader)
		if err != nil {
			log.Error("Decoding error %s\n", err)
			log.Debug("Stream (%d) % x", len(obj.Stream), obj.Stream)
			return nil, err
		}
		defer r.Close()

		var outBuf bytes.Buffer
		outBuf.ReadFrom(r)
		outData := outBuf.Bytes()

		if hasDecodeParams && predictor != 1 {
			if predictor == 2 { // TIFF encoding: Needs some tests.
				log.Debug("Tiff encoding")

				columns, ok := (*decodeParams)["Columns"].(*PdfObjectInteger)
				if !ok {
					log.Error("Predictor Column missing\n")
					return nil, fmt.Errorf("Predictor column missing")
				}

				colors := 1
				pcolors, hascolors := (*decodeParams)["Colors"].(*PdfObjectInteger)
				if hascolors {
					// Number of interleaved color components per sample
					colors = int(*pcolors)
				}
				log.Debug("colors: %d", colors)

				rowLength := int(*columns) * colors
				rows := len(outData) / rowLength
				if len(outData)%rowLength != 0 {
					log.Error("TIFF encoding: Invalid row length...")
					return nil, fmt.Errorf("Invalid row length (%d/%d)", len(outData), rowLength)
				}

				if rowLength%colors != 0 {
					return nil, fmt.Errorf("Invalid row length (%d) for colors %d", rowLength, colors)
				}
				log.Debug("inp outData (%d): % x", len(outData), outData)

				pOutBuffer := bytes.NewBuffer(nil)

				// 0-255  -255 255 ; 0-255=-255;
				for i := 0; i < rows; i++ {
					rowData := outData[rowLength*i : rowLength*(i+1)]
					//log.Debug("RowData before: % d", rowData)
					// Predicts the same as the sample to the left.
					// Interleaved by colors.
					for j := colors; j < rowLength; j++ {
						rowData[j] = byte(int(rowData[j]+rowData[j-colors]) % 256)
					}
					// GH: Appears that this is not working as expected...
					//log.Debug("RowData after: % d", rowData)

					pOutBuffer.Write(rowData)
				}
				pOutData := pOutBuffer.Bytes()
				log.Debug("POutData (%d): % x", len(pOutData), pOutData)
				return pOutData, nil
			} else if predictor >= 10 && predictor <= 15 {
				log.Debug("PNG Encoding")
				columns, ok := (*decodeParams)["Columns"].(*PdfObjectInteger)
				if !ok {
					log.Error("Predictor Column missing\n")
					return nil, fmt.Errorf("Predictor column missing")
				}
				rowLength := int(*columns + 1) // 1 byte to specify predictor algorithms per row.
				rows := len(outData) / rowLength
				if len(outData)%rowLength != 0 {
					log.Error("Invalid row length...")
					return nil, fmt.Errorf("Invalid row length (%d/%d)", len(outData), rowLength)
				}

				pOutBuffer := bytes.NewBuffer(nil)

				log.Debug("Predictor columns: %d", columns)
				log.Debug("Length: %d / %d = %d rows", len(outData), rowLength, rows)
				prevRowData := make([]byte, rowLength)
				for i := 0; i < rowLength; i++ {
					prevRowData[i] = 0
				}

				for i := 0; i < rows; i++ {
					rowData := outData[rowLength*i : rowLength*(i+1)]

					fb := rowData[0]
					switch fb {
					case 0:
						// No prediction. (No operation).
					case 1:
						// Sub: Predicts the same as the sample to the left.
						for j := 2; j < rowLength; j++ {
							rowData[j] = byte(int(rowData[j]+rowData[j-1]) % 256)
						}
					case 2:
						// Up: Predicts the same as the sample above
						for j := 1; j < rowLength; j++ {
							rowData[j] = byte(int(rowData[j]+prevRowData[j]) % 256)
						}
					default:
						log.Error("Invalid filter byte (%d)", fb)
						return nil, fmt.Errorf("Invalid filter byte (%d)", fb)
					}

					for i := 0; i < rowLength; i++ {
						prevRowData[i] = rowData[i]
					}
					pOutBuffer.Write(rowData[1:])
				}
				pOutData := pOutBuffer.Bytes()
				return pOutData, nil
			} else {
				log.Error("Unsupported predictor (%d)", predictor)
				return nil, fmt.Errorf("Unsupported predictor (%d)", predictor)
			}
		}

		return outData, nil
	} else if *method == "ASCIIHexDecode" {
		bufReader := bytes.NewReader(obj.Stream)
		inb := []byte{}
		for {
			b, err := bufReader.ReadByte()
			if err != nil {
				return nil, err
			}
			if b == '>' {
				break
			}
			if isWhiteSpace(b) {
				continue
			}
			if (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F') || (b >= '0' && b <= '9') {
				inb = append(inb, b)
			} else {
				log.Error("Invalid ascii hex character (%c)", b)
				return nil, fmt.Errorf("Invalid ascii hex character (%c)", b)
			}
		}
		if len(inb)%2 == 1 {
			inb = append(inb, '0')
		}
		log.Debug("Inbound %s", inb)
		outb := make([]byte, hex.DecodedLen(len(inb)))
		_, err := hex.Decode(outb, inb)
		if err != nil {
			return nil, err
		}
		return outb, nil
	}

	log.Error("Unsupported encoding method!")
	return nil, fmt.Errorf("Unsupported encoding method")
}
