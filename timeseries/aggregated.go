package timeseries

import (
	"strings"
)

type aggregatingIterator struct {
	input   []Iterator
	aggFunc F
}

func (i *aggregatingIterator) Next() bool {
	for _, iter := range i.input {
		if !iter.Next() {
			return false
		}
	}
	return true
}

func (i *aggregatingIterator) Value() (Time, float64) {
	acc := NaN
	if len(i.input) == 2 {
		t, v1 := i.input[0].Value()
		_, v2 := i.input[1].Value()
		return t, i.aggFunc(t, v1, v2)
	}
	var v float64
	var t Time
	for _, iter := range i.input {
		t, v = iter.Value()
		acc = i.aggFunc(t, acc, v)
	}
	return t, acc
}

type AggregatedTimeseries struct {
	input   []TimeSeries
	aggFunc F
}

func (ts *AggregatedTimeseries) AddInput(tss ...TimeSeries) *AggregatedTimeseries {
	for _, t := range tss {
		if t == nil {
			continue
		}
		ts.input = append(ts.input, t)
	}
	return ts
}

func (ts *AggregatedTimeseries) len() int {
	for _, i := range ts.input {
		if i != nil {
			return i.len()
		}
	}
	return 0
}

func (ts *AggregatedTimeseries) last() float64 {
	return Reduce(func(t Time, accumulator, v float64) float64 {
		return v
	}, ts)
}

func (ts *AggregatedTimeseries) isEmpty() bool {
	return len(ts.input) == 0
}

func (ts *AggregatedTimeseries) String() string {
	values := make([]string, 0)
	iter := ts.iter()
	for iter.Next() {
		_, v := iter.Value()
		values = append(values, Value(v).String())
	}
	return "AggregatedTimeseries(" + strings.Join(values, " ") + ")"
}

func (ts *AggregatedTimeseries) iter() Iterator {
	iter := &aggregatingIterator{aggFunc: ts.aggFunc}
	for _, i := range ts.input {
		if i != nil {
			iIter := i.iter()
			if _, ok := iIter.(*NilIterator); !ok {
				iter.input = append(iter.input, iIter)
			}
		}
	}
	if len(iter.input) == 0 {
		return &NilIterator{}
	}
	return iter
}

func (ts *AggregatedTimeseries) MarshalJSON() ([]byte, error) {
	return MarshalJSON(ts)
}
