package utils

import "io"

type CountingReader struct {
	R io.Reader
	N int64
}

func (r *CountingReader) Read(p []byte) (n int, err error) {
	n, err = r.R.Read(p)
	r.N += int64(n)
	return n, err
}
