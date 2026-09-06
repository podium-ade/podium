package conductor

import (
	"context"
	"time"
)

// extractionCheckInterval is how often the conductor asks the shared memory what became of
// the retains it handed over. Extraction is background work measured in seconds and nothing
// waits on the answer, so this is slow on purpose: it exists to make a sustained outage
// visible, not to track individual turns.
const extractionCheckInterval = 5 * time.Minute

// extractionCheckTimeout bounds one check. It is one GET of a small page.
const extractionCheckTimeout = 15 * time.Second

// failuresLogged caps how many failed operations one log line names. A total outage fails
// every retain, and a hundred identical lines say nothing the first two do not.
const failuresLogged = 3

// watchExtractions reports retains that Hindsight accepted and then could not finish.
//
// This exists because Retain is asynchronous and its success means only that the work was
// TAKEN. Extraction runs afterwards, in Hindsight's own worker, against a model API this
// process never calls — and when that fails, it fails for every retain, forever, while the
// conductor goes on logging accepted hand-offs and turns go on looking healthy. An install
// can be in that state for weeks: the service answers /health, the bank is simply empty.
//
// It reads the shared memory rather than tracking the ids it handed over, which also covers
// retains a task container made for itself over MCP. Nothing here fails a turn or blocks a
// shutdown: it is a gauge and a log line.
func (c *Conductor) watchExtractions(ctx context.Context) {
	if c.memories == nil {
		return
	}
	ticker := time.NewTicker(extractionCheckInterval)
	defer ticker.Stop()
	for {
		c.checkExtractions(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// checkExtractions is one pass. Separated so a test can run it without a clock.
func (c *Conductor) checkExtractions(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, extractionCheckTimeout)
	defer cancel()

	failed, err := c.memories.FailedOperations(checkCtx, failuresLogged)
	if err != nil {
		// Not counted as an extraction failure: this is the check itself failing, which
		// Ready already reports on /readyz.
		c.logger.WarnContext(checkCtx, "could not ask the shared memory what became of its retains",
			"error", err)
		return
	}

	c.metrics.MemoryExtractionsFailed.Set(float64(len(failed)))
	if len(failed) == 0 {
		return
	}

	// The document id is the turn id for anything this conductor retained, so a human can
	// go from here to the conversation. The error is Hindsight's own words and names the
	// cause — a rejected model key reads as an authentication error, and that is the whole
	// diagnosis.
	for _, op := range failed {
		c.logger.WarnContext(checkCtx,
			"the shared memory accepted a retain and then extracted nothing from it: "+
				"this memory is lost, and the turn that produced it looked fine",
			"operation_id", op.ID, "document_id", op.DocumentID,
			"retries", op.RetryCount, "error", op.ErrorMessage)
	}
}
