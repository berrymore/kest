package rrhttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/berrymore/kest/internal/worker"
)

// ErrorStatus maps an error from handling a request to the HTTP status kest
// answers with:
//
//	*http.MaxBytesError                         413
//	*RequestError                               400
//	ErrQueueFull, ErrNoWorker, ErrStopped        503
//	ErrExecTimeout, context.DeadlineExceeded     504 (exec_ttl or the route timeout)
//	*JobError ($worker->error())                500
//	anything else: *ProcessError, ErrStopRequested,
//	  *ResponseError                            502
//
// RoadRunner answers 500 to all worker errors.
func ErrorStatus(err error) int {
	switch {
	case isType[*http.MaxBytesError](err):
		return http.StatusRequestEntityTooLarge
	case isType[*RequestError](err):
		return http.StatusBadRequest
	case errors.Is(err, worker.ErrQueueFull), errors.Is(err, worker.ErrNoWorker), errors.Is(err, worker.ErrStopped):
		return http.StatusServiceUnavailable
	case errors.Is(err, worker.ErrExecTimeout), errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case isType[*worker.JobError](err):
		return http.StatusInternalServerError
	default:
		return http.StatusBadGateway
	}
}

// WriteError answers with the status for err and its status text, nothing
// more: details belong in the log, not in the response.
func WriteError(w http.ResponseWriter, err error) int {
	status := ErrorStatus(err)
	http.Error(w, http.StatusText(status), status)
	return status
}

func isType[E error](err error) bool {
	_, ok := errors.AsType[E](err)
	return ok
}
