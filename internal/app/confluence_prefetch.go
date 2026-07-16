package app

import (
	"context"
	"fmt"

	"github.com/isukharev/atl/internal/domain"
)

type pagePrefetchResult struct {
	index int
	page  *domain.Resource
	err   error
}

// orderedPagePrefetch overlaps only remote native-body reads. The caller still
// performs every path claim, asset/comment/macro resolution, mirror write,
// relocation, sidecar flush, and checkpoint mutation sequentially.
type orderedPagePrefetch struct {
	ctx     context.Context
	cancel  context.CancelFunc
	jobs    chan int
	results chan pagePrefetchResult
	ids     []string
	nextJob int
	next    int
	pending map[int]pagePrefetchResult
}

func newOrderedPagePrefetch(ctx context.Context, store domain.DocStore, ids []string, workers int, includeRestrictions bool) *orderedPagePrefetch {
	workerCtx, cancel := context.WithCancel(ctx)
	p := &orderedPagePrefetch{
		ctx: workerCtx, cancel: cancel, jobs: make(chan int, workers), results: make(chan pagePrefetchResult, workers),
		ids: ids, pending: make(map[int]pagePrefetchResult, workers),
	}
	for range workers {
		go func() {
			for index := range p.jobs {
				page, err := store.GetPage(workerCtx, ids[index], domain.PullOpts{Format: "csf", IncludeRestrictions: includeRestrictions})
				select {
				case p.results <- pagePrefetchResult{index: index, page: page, err: err}:
				case <-workerCtx.Done():
					return
				}
			}
		}()
	}
	for p.nextJob < len(ids) && p.nextJob < workers {
		p.jobs <- p.nextJob
		p.nextJob++
	}
	return p
}

func (p *orderedPagePrefetch) nextPage(id string) (*domain.Resource, error) {
	if p == nil || p.next >= len(p.ids) || p.ids[p.next] != id {
		return nil, fmt.Errorf("ordered page prefetch identity mismatch")
	}
	for {
		if result, ok := p.pending[p.next]; ok {
			delete(p.pending, p.next)
			p.next++
			p.dispatchOne()
			return result.page, result.err
		}
		select {
		case result := <-p.results:
			p.pending[result.index] = result
		case <-p.ctx.Done():
			return nil, p.ctx.Err()
		}
	}
}

func (p *orderedPagePrefetch) dispatchOne() {
	if p.nextJob >= len(p.ids) {
		return
	}
	p.jobs <- p.nextJob
	p.nextJob++
}

func (p *orderedPagePrefetch) close() {
	if p == nil {
		return
	}
	p.cancel()
	close(p.jobs)
}
