package confluence

// CloseIdleConnections closes pooled transport connections during an owning
// long-running host's ordered shutdown.
func (cf *Confluence) CloseIdleConnections() {
	if cf != nil && cf.c != nil {
		cf.c.CloseIdleConnections()
	}
}
