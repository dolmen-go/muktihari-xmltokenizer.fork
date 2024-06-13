package xmltokenizer

import (
	"errors"
	"io"
)

// Tokenizer is a XML tokenizer.
type Tokenizer struct {
	r         io.Reader // reader provided by the client
	options   options   // tokenizer's options
	buf       []byte    // buffer that will grow as needed
	cur, last int       // cur and last bytes positions
	err       error     // last encountered error
	token     Token     // shared token
}

type options struct {
	readBufferSize  int
	attrsBufferSize int
}

func defaultOptions() options {
	return options{
		readBufferSize:  4096,
		attrsBufferSize: 8,
	}
}

// Option is Tokenizer option.
type Option func(o *options)

// WithReadBufferSize directs XML Tokenizer to this buffer size
// to read from the io.Reader. Default: 4096.
func WithReadBufferSize(size int) Option {
	return func(o *options) { o.readBufferSize = size }
}

// WithAttrBufferSize directs XML Tokenizer to use this Attrs
// buffer capacity as its initial size. Default: 8.
func WithAttrBufferSize(size int) Option {
	return func(o *options) { o.attrsBufferSize = size }
}

// New creates new XML tokenizer.
func New(r io.Reader, opts ...Option) *Tokenizer {
	t := new(Tokenizer)
	t.Reset(r, opts...)
	return t
}

// Reset resets the Tokenizer, maintaining storage for
// future tokenization to reduce memory alloc.
func (t *Tokenizer) Reset(r io.Reader, opts ...Option) {
	t.r, t.err = r, nil
	t.options = defaultOptions()
	for i := range opts {
		opts[i](&t.options)
	}
	if cap(t.token.Attrs) < t.options.attrsBufferSize {
		t.token.Attrs = make([]Attr, 0, t.options.attrsBufferSize)
	}
}

// Token returns either a valid token or an error.
// The returned token is only valid before next
// Token or RawToken method invocation.
func (t *Tokenizer) Token() (Token, error) {
	if t.err != nil {
		return Token{}, t.err
	}

	b, err := t.RawToken()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			return Token{}, err
		}
		t.err = io.EOF
	}

	t.clearToken()

	b = t.consumeTagName(b)
	b = t.consumeAttrs(b)
	t.token.CharData = trim(b)

	return t.token, nil
}

// RawToken returns token in its raw bytes. At the end,
// it may returns last token bytes and an io.EOF error.
// The returned token bytes is only valid before next
// Token or RawToken method invocation.
func (t *Tokenizer) RawToken() (b []byte, err error) {
	pos := t.cur
	var off int
	for {
		if pos >= t.last {
			t.memmoveRemainingBytes(off)
			pos, off = t.last, 0
			if err = t.manageBuffer(); err != nil {
				return nil, err
			}
		}
		switch t.buf[pos] {
		case '<':
			off = pos
		case '>':
		loop:
			// If next char represent CharData, include it.
			for i := pos + 1; ; i++ {
				if i >= t.last {
					t.memmoveRemainingBytes(off)
					i, pos, off = t.last, 0, 0
					if err = t.manageBuffer(); err != nil {
						pos = i
						break
					}
				}
				if t.buf[i] == '<' {
					pos = i
					break loop
				}
			}
			buf := trim(t.buf[off:pos:cap(t.buf)])
			t.cur = pos
			return buf, err
		}
		pos++
	}
}

func (t *Tokenizer) clearToken() {
	t.token.Name.Space = nil
	t.token.Name.Local = nil
	t.token.Name.Full = nil
	t.token.Attrs = t.token.Attrs[:0]
	t.token.CharData = nil
	t.token.SelfClosing = false
}

func (t *Tokenizer) memmoveRemainingBytes(off int) {
	n := copy(t.buf, t.buf[off:])
	t.buf = t.buf[:n:cap(t.buf)]
	t.cur, t.last = 0, n
}

func (t *Tokenizer) manageBuffer() error {
	var start, end int
	bufferSize := t.options.readBufferSize
	switch {
	case t.buf == nil:
		// Create buffer twice of size in case we need to memmove remaining bytes
		t.buf = make([]byte, bufferSize, bufferSize*2)
		end = bufferSize
	case t.last+bufferSize <= cap(t.buf):
		// Grow by reslice
		t.buf = t.buf[: t.last+bufferSize : cap(t.buf)]
		start, end = t.last, t.last+bufferSize
	default:
		// Grow by make new alloc
		buf := make([]byte, t.last+bufferSize)
		n := copy(buf, t.buf)
		t.buf = buf
		start, end = n, cap(t.buf)
	}

	n, err := io.ReadAtLeast(t.r, t.buf[start:end], 1)
	if err != nil {
		return err
	}
	t.buf = t.buf[: start+n : cap(t.buf)]
	t.last = len(t.buf)

	return nil
}

func (t *Tokenizer) consumeTagName(b []byte) []byte {
	var pos, fullpos int
	for i := range b {
		switch b[i] {
		case '<':
			pos = i + 1
			fullpos = i + 1
		case ':':
			t.token.Name.Space = trim(b[pos:i])
			pos = i + 1
		case '>', ' ': // e.g. <gpx>, <trkpt lat="-7.1872750" lon="110.3450230">
			t.token.Name.Local = trim(b[pos:i])
			t.token.Name.Full = trim(b[fullpos:i])
			return b[i+1:]
		}
	}
	return b
}

func (t *Tokenizer) consumeAttrs(b []byte) []byte {
	var space, local, full []byte
	var pos, fullpos int
	var inquote bool
	for i := range b {
		switch b[i] {
		case ':':
			if !inquote {
				space = trim(b[pos:i])
				pos = i + 1
			}
		case '=':
			local = trim(b[pos:i])
			full = trim(b[fullpos:i])
			pos = i + 1
		case '"':
			inquote = !inquote
			if !inquote {
				if full == nil {
					continue
				}
				t.token.Attrs = append(t.token.Attrs, Attr{
					Name:  Name{Space: space, Local: local, Full: full},
					Value: trim(b[pos+1 : i]),
				})
				space, local, full = nil, nil, nil
				pos = i + 1
				fullpos = i + 1
			}
		case '>':
			if b[i-1] == '/' {
				t.token.SelfClosing = true
			}
			return b[i+1:]
		}
	}
	return b
}

func trim(b []byte) []byte {
	var start int
start:
	for i := range b {
		switch b[i] {
		case '\r':
			if i+1 < len(b) && b[i+1] == '\n' {
				start += 2
			}
		case '\n':
			start++
		case ' ':
			start++
		default:
			break start
		}
	}
	b = b[start:]

	var end int = len(b)
end:
	for i := len(b) - 1; i >= 0; i-- {
		switch b[i] {
		case '\n':
			end--
			if i-1 > 0 && b[i-1] == '\r' {
				end--
			}
		case ' ':
			end--
		default:
			break end
		}
	}

	return b[:end]
}