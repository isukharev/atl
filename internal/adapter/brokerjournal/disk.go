package brokerjournal

// disk keeps all access relative to the same verified held directory handles.
// No method returns an underlying filesystem error: it may contain a private
// path. A failed mutation is ambiguous and poisons the owning Journal.
type disk interface {
	check() error
	read(string, int64) ([]byte, error)
	allocate(string, int64) error
	write(string, int64, []byte) error
	names(int) ([]string, error)
	close() error
}
