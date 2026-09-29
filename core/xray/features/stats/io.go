package stats

type IOLease interface {
	EndIO()
}

func BeginIO(counter Counter) (IOLease, error) {
	if guarded, ok := counter.(interface{ BeginIO() (IOLease, error) }); ok {
		return guarded.BeginIO()
	}
	return nil, nil
}

func EndIO(lease IOLease) {
	if lease != nil {
		lease.EndIO()
	}
}
