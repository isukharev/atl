package jira

// CloseIdleConnections closes pooled transport connections during an owning
// long-running host's ordered shutdown.
func (j *Jira) CloseIdleConnections() {
	if j != nil && j.c != nil {
		j.c.CloseIdleConnections()
	}
}
