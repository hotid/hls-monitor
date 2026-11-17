package api

import (
	"context"
	"time"
)

// seriesSettle is how long after a deletion samples of what was deleted
// may still reach the time series storage: results being stored at that
// moment, and what waits in the server's memory.
const seriesSettle = 2 * time.Minute

// PurgeSeries deletes from the time series storage the series of streams
// and agents that were deleted. The storage may be away, and samples may
// still be on their way to it, so a deletion is repeated until it has
// succeeded after everything older than it has been written.
func (s *Server) PurgeSeries(ctx context.Context) {
	pending, err := s.st.SeriesDeletions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.log.Error("cannot list series to delete", "error", err)
		}
		return
	}
	for _, d := range pending {
		if err := s.mx.DeleteSeries(ctx, d.Label, d.ID); err != nil {
			s.log.Warn("cannot delete series yet, will retry", d.Label, d.ID, "error", err)
			return // the storage is away: the rest would fail as well
		}
		if s.mx.Backlog() || s.now().Sub(d.CreatedAt) < seriesSettle {
			continue // samples of it may still arrive; once more later
		}
		if err := s.st.SeriesDeleted(ctx, d.Label, d.ID); err != nil && ctx.Err() == nil {
			s.log.Error("cannot record that series were deleted", "error", err)
		}
	}
}

// purgeSeriesSoon runs PurgeSeries in the background: it must not hold up
// the request that deleted something.
func (s *Server) purgeSeriesSoon() {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.PurgeSeries(ctx)
	}()
}
