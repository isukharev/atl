package telemetry

import "io"

// WriteLocalSpool writes canonical bytes to an explicitly supplied
// owner-controlled writer. It never opens a path, discovers an endpoint, or
// performs network I/O; choosing a local file is the caller's explicit
// authority boundary.
func WriteLocalSpool(writer io.Writer, projection Projection) error {
	if writer == nil {
		return fail(ErrorInvalidSpool)
	}
	data, err := Encode(projection)
	if err != nil {
		return err
	}
	for len(data) > 0 {
		written, writeErr := writer.Write(data)
		if written < 0 || written > len(data) {
			return fail(ErrorInvalidSpool)
		}
		if writeErr != nil {
			return fail(ErrorInvalidSpool)
		}
		if written == 0 {
			return fail(ErrorInvalidSpool)
		}
		data = data[written:]
	}
	return nil
}
