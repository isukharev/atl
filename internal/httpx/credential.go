package httpx

// ClearCredential releases the bearer retained by a short-lived client. The
// caller must ensure no requests are active while clearing it.
func (c *Client) ClearCredential() {
	if c != nil {
		c.token = ""
	}
}
