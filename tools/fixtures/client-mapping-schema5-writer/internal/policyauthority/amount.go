package policyauthority

const fractionScale = 1000000

// Fixed point operations never multiply the whole-byte component, keeping
// signed SQL bounds valid even close to MaxInt64.
type amount struct{ whole, fraction uint64 }

func (a amount) less(b amount) bool {
	return a.whole < b.whole || a.whole == b.whole && a.fraction < b.fraction
}

func addAmounts(values ...amount) (amount, error) {
	var total amount
	for _, a := range values {
		if a.fraction >= fractionScale {
			return amount{}, ErrCapacity
		}
		total.fraction += a.fraction
		whole, err := sum(total.whole, a.whole, total.fraction/fractionScale)
		if err != nil {
			return amount{}, err
		}
		total.whole, total.fraction = whole, total.fraction%fractionScale
	}
	return total, nil
}

func subtractAmounts(a, b amount) (amount, error) {
	if a.fraction >= fractionScale || b.fraction >= fractionScale || !bounded(a.whole, b.whole) || a.less(b) {
		return amount{}, ErrCapacity
	}
	whole, fraction := a.whole-b.whole, a.fraction
	if fraction < b.fraction {
		whole--
		fraction += fractionScale
	}
	return amount{whole, fraction - b.fraction}, nil
}

func usageAmount(u Usage) amount { return amount{u.BilledBytes, u.Remainder} }

func usageDelta(after, before Usage) (Usage, error) {
	if after.RawUpload < before.RawUpload || after.RawDownload < before.RawDownload {
		return Usage{}, ErrRequest
	}
	billed, err := subtractAmounts(usageAmount(after), usageAmount(before))
	if err != nil {
		return Usage{}, ErrRequest
	}
	return Usage{RawUpload: after.RawUpload - before.RawUpload, RawDownload: after.RawDownload - before.RawDownload, BilledBytes: billed.whole, Remainder: billed.fraction}, nil
}

func addUsage(base, delta Usage) (Usage, error) {
	u, err := sum(base.RawUpload, delta.RawUpload)
	if err != nil {
		return Usage{}, err
	}
	d, err := sum(base.RawDownload, delta.RawDownload)
	if err != nil {
		return Usage{}, err
	}
	b, err := addAmounts(usageAmount(base), usageAmount(delta))
	if err != nil {
		return Usage{}, err
	}
	return Usage{RawUpload: u, RawDownload: d, BilledBytes: b.whole, Remainder: b.fraction}, nil
}
