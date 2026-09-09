package revision

import (
	"context"
	"github.com/perber/wiki/internal/core/tree"
)

// Bind keeps the existing revision algorithms on the same page transaction.
func (s *Service) Bind(pages *tree.TreeService) *Service {
	if s == nil {
		return nil
	}
	local := NewService(s.storageDir, pages, s.log, ServiceOptions{MaxRevisions: s.maxRevisions, CoalesceWindow: s.coalesceWindow})
	if ctx, db := pages.TransactionDB(); db != nil {
		local.store = &postgresStore{FSStore: NewFSStore(s.storageDir, s.log), db: db, ctx: ctx}
	}
	return local
}
func revisionRead[T any](s *Service, fn func(*Service) (T, error)) (T, error) {
	var out T
	e := s.pages.ReadTransaction(context.Background(), func(p *tree.TreeService) error { var e error; out, e = fn(s.Bind(p)); return e })
	return out, e
}

func revisionWrite(s *Service, fn func(*Service) (*Revision, bool, error)) (*Revision, bool, error) {
	var r *Revision
	var changed bool
	e := s.pages.Transact(context.Background(), func(p *tree.TreeService) error { var e error; r, changed, e = fn(s.Bind(p)); return e })
	return r, changed, e
}
