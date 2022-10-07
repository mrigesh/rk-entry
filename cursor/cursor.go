package rkcursor

import (
	"bytes"
	"fmt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/rookie-ninja/rk-entry/v3/entry"
	"github.com/rookie-ninja/rk-entry/v3/middleware"
	"github.com/rookie-ninja/rk-query/v2"
	"go.uber.org/zap"
	"runtime"
	"strings"
	"sync"
	"time"
)

const metricsKey = "elapsedNano"

var (
	summaryVec *prometheus.SummaryVec
	logger     *zap.Logger
	label      *promLabel
)

func init() {
	// 1: init labels
	label = &promLabel{
		keys: []string{
			"entryName",
			"entryType",
			"domain",
			"instance",
			"parent",
			"operation",
			"status",
		},
		values: []string{
			"",
			"",
			rkmid.Domain.String,
			rkmid.LocalIp.String,
		},
	}

	// 2: init summary vec and register to default registerer
	opts := prometheus.SummaryOpts{
		Namespace:  "rk",
		Subsystem:  "cursor",
		Name:       metricsKey,
		Objectives: map[float64]float64{0.5: 0.05, 0.9: 0.01, 0.99: 0.001, 0.999: 0.0001},
		Help:       fmt.Sprintf("Summary of cursor with labels:%s", label.keys),
	}
	summaryVec = prometheus.NewSummaryVec(opts, label.keys)
	prometheus.DefaultRegisterer.Register(summaryVec)

	// 3: init logger
	logger = rk.NewZapEntryStdout().Logger
}

// ************* Global *************

func OverrideEntryNameAndType(entryName, entryType string) {
	label.mutex.Lock()
	defer label.mutex.Unlock()

	label.values[0] = entryName
	label.values[1] = entryType
}

func OverrideLogger(l *zap.Logger) {
	if l != nil {
		logger = l
	}
}

func SummaryVec() *prometheus.SummaryVec {
	return summaryVec
}

func Click() *pointer {
	return &pointer{
		start:     time.Now(),
		parent:    parentName(),
		operation: operationName(),
	}
}

func Error(err error) {
	if err == nil {
		return
	}

	stack := stacks()

	var builder bytes.Buffer

	// print error message
	builder.WriteString(fmt.Sprintf("%s\n", err.Error()))
	// print stack function
	for i := range stack {
		pc := stack[i] - 1
		builder.WriteString(fmt.Sprintf("%d)\t%s\n", i, fileline(pc)))
	}

	logger.WithOptions(zap.AddCallerSkip(1)).Error(builder.String())
}

func AddField(key, val string) {
	logger = logger.With(zap.String(key, val))
}

// ************* Instance *************

type Option func(c *Cursor)

func WithEntryNameAndType(entryName, entryType string) Option {
	return func(c *Cursor) {
		c.entryName = entryName
		c.entryType = entryType
	}
}

func WithLogger(l *zap.Logger) Option {
	return func(c *Cursor) {
		if l != nil {
			c.Logger = l
		}
	}
}

func WithEvent(e rkquery.Event) Option {
	return func(c *Cursor) {
		if e != nil {
			c.Event = e
		}
	}
}

func NewCursor(opts ...Option) *Cursor {
	c := &Cursor{
		Logger:    rk.NewZapEntryStdout().Logger,
		Event:     rk.NewEventEntryNoop().CreateEventNoop(),
		entryName: "",
		entryType: "",
		Now:       time.Now(),
	}

	for i := range opts {
		opts[i](c)
	}

	return c
}

type Cursor struct {
	Logger    *zap.Logger
	Event     rkquery.Event
	Now       time.Time
	entryName string
	entryType string
}

func (c *Cursor) Click() *pointer {
	operation := operationName()

	if c.Event != nil {
		c.Event.StartTimer(operation)
	}

	return &pointer{
		entryName: c.entryName,
		entryType: c.entryType,
		start:     time.Now(),
		operation: operation,
		parent:    parentName(),
		logger:    c.Logger,
		event:     c.Event,
	}
}

func (c *Cursor) Error(err error) {
	if err == nil {
		return
	}

	stack := stacks()

	var builder bytes.Buffer

	// print error message
	builder.WriteString(fmt.Sprintf("%s\n", err.Error()))
	// print stack function
	for i := range stack {
		pc := stack[i] - 1
		builder.WriteString(fmt.Sprintf("%d)\t%s\n", i, fileline(pc)))
	}

	c.Logger.WithOptions(zap.AddCallerSkip(1)).Error(builder.String())
}

func (c *Cursor) AddField(key, val string) {
	c.Logger = c.Logger.With(zap.String(key, val))
	c.Event.AddPair(key, val)
}

// ************* Prometheus labels *************

type promLabel struct {
	mutex  sync.Mutex
	keys   []string
	values []string
}

func (l *promLabel) getValues(parent, op, entryName, entryType string, err error) []string {
	label.mutex.Lock()
	defer label.mutex.Unlock()

	status := "OK"
	if err != nil {
		status = "ERROR"
	}

	res := append(l.values, parent, op, status)
	res[0] = entryName
	res[1] = entryType

	return res
}

// ************* Cursor *************

type pointer struct {
	start     time.Time
	parent    string
	operation string
	err       error
	event     rkquery.Event
	logger    *zap.Logger
	entryName string
	entryType string
}

func (c *pointer) ObserveError(err error) error {
	if err == nil {
		return nil
	}

	c.err = err

	stack := stacks()

	var builder bytes.Buffer

	// print error message
	builder.WriteString(fmt.Sprintf("%s\n", err.Error()))
	// print stack function
	for i := range stack {
		pc := stack[i] - 1
		builder.WriteString(fmt.Sprintf("%d)\t%s\n", i, fileline(pc)))
	}

	if c.logger != nil {
		c.logger.WithOptions(zap.AddCallerSkip(1)).Error(builder.String())
	} else {
		logger.WithOptions(zap.AddCallerSkip(1)).Error(builder.String())
	}

	if c.event != nil {
		c.event.IncCounter(strings.Join([]string{c.operation, "ERROR"}, "."), 1)
	}

	return err
}

func (c *pointer) Release() {
	elapsedNano := time.Now().Sub(c.start).Nanoseconds()

	observer, _ := summaryVec.GetMetricWithLabelValues(
		label.getValues(c.parent, c.operation, c.entryName, c.entryType, c.err)...)
	if observer == nil {
		return
	}

	if c.event != nil {
		c.event.EndTimer(c.operation)
	}

	observer.Observe(float64(elapsedNano))
}

// ************* helper functions *************

func operationName() string {
	pc, _, _, ok := runtime.Caller(2)
	if !ok {
		return "unknown"
	}

	fName := runtime.FuncForPC(pc).Name()
	// 1: try to check whether it is nested, trim prefix of file path
	fName = fName[strings.LastIndex(fName, "/")+1:]
	fName = strings.ReplaceAll(fName, "(", "")
	fName = strings.ReplaceAll(fName, ")", "")
	fName = strings.ReplaceAll(fName, "*", "")

	return fName
}

func parentName() string {
	pc, file, _, ok := runtime.Caller(3)
	if !ok {
		return "-"
	}

	fName := runtime.FuncForPC(pc).Name()
	if strings.Contains(file, "@") {
		return "-"
	}

	// 1: try to check whether it is nested, trim prefix of file path
	fName = fName[strings.LastIndex(fName, "/")+1:]
	fName = strings.ReplaceAll(fName, "(", "")
	fName = strings.ReplaceAll(fName, ")", "")
	fName = strings.ReplaceAll(fName, "*", "")

	return fName
}

func stacks() []uintptr {
	const depth = 32
	var pcs [depth]uintptr
	n := runtime.Callers(3, pcs[:])

	index := n
	for i := range pcs[:n] {
		pc := pcs[i]
		if strings.Contains(file(pc), "@") {
			index = i
			break
		}
	}

	return pcs[:index]
}

func file(pc uintptr) string {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "unknown"
	}

	file, _ := fn.FileLine(pc)
	return file
}

func fileline(pc uintptr) string {
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return "unknown"
	}

	file, line := fn.FileLine(pc)
	return fmt.Sprintf("%s\t%s:%d", fn.Name(), file, line)
}
