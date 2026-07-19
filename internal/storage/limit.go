package storage

import (
	"context"
	"io"

	"golang.org/x/time/rate"
)

type limitedReader struct {
	context context.Context
	reader  io.Reader
	limiter *rate.Limiter
}

func newLimitedReader(ctx context.Context, reader io.Reader, bytesPerSecond int64) io.Reader {
	if bytesPerSecond <= 0 {
		return reader
	}
	burst := int(bytesPerSecond)
	if burst > 4*1024*1024 {
		burst = 4 * 1024 * 1024
	}
	if burst < 32*1024 {
		burst = 32 * 1024
	}
	return &limitedReader{
		context: ctx,
		reader:  reader,
		limiter: rate.NewLimiter(rate.Limit(bytesPerSecond), burst),
	}
}

func (r *limitedReader) Read(buffer []byte) (int, error) {
	if len(buffer) > r.limiter.Burst() {
		buffer = buffer[:r.limiter.Burst()]
	}
	n, err := r.reader.Read(buffer)
	if n > 0 {
		if waitErr := r.limiter.WaitN(r.context, n); waitErr != nil {
			return 0, waitErr
		}
	}
	return n, err
}

type limitedReadCloser struct {
	io.Reader
	closer io.Closer
}

func (r *limitedReadCloser) Close() error {
	return r.closer.Close()
}
