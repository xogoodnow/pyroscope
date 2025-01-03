package otlp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/grafana/pyroscope/pkg/distributor/model"

	"github.com/prometheus/prometheus/util/testutil"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1experimental2 "github.com/grafana/pyroscope/api/otlp/collector/profiles/v1development"
	v1experimental "github.com/grafana/pyroscope/api/otlp/profiles/v1development"
	"github.com/grafana/pyroscope/pkg/og/convert/pprof/bench"
	"github.com/grafana/pyroscope/pkg/test/mocks/mockotlp"

	"github.com/stretchr/testify/assert"

	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	v1 "github.com/grafana/pyroscope/api/otlp/common/v1"
)

func TestGetServiceNameFromAttributes(t *testing.T) {
	tests := []struct {
		name     string
		attrs    []v1.KeyValue
		expected string
	}{
		{
			name:     "empty attributes",
			attrs:    []v1.KeyValue{},
			expected: "unknown",
		},
		{
			name: "service name present",
			attrs: []v1.KeyValue{
				{
					Key: "service.name",
					Value: v1.AnyValue{
						Value: &v1.AnyValue_StringValue{
							StringValue: "test-service",
						},
					},
				},
			},
			expected: "test-service",
		},
		{
			name: "service name empty",
			attrs: []v1.KeyValue{
				{
					Key: "service.name",
					Value: v1.AnyValue{
						Value: &v1.AnyValue_StringValue{
							StringValue: "",
						},
					},
				},
			},
			expected: "unknown",
		},
		{
			name: "service name among other attributes",
			attrs: []v1.KeyValue{
				{
					Key: "host.name",
					Value: v1.AnyValue{
						Value: &v1.AnyValue_StringValue{
							StringValue: "host1",
						},
					},
				},
				{
					Key: "service.name",
					Value: v1.AnyValue{
						Value: &v1.AnyValue_StringValue{
							StringValue: "test-service",
						},
					},
				},
			},
			expected: "test-service",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getServiceNameFromAttributes(tt.attrs)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestAppendAttributesUnique(t *testing.T) {
	tests := []struct {
		name          string
		existingAttrs []*typesv1.LabelPair
		newAttrs      []v1.KeyValue
		processedKeys map[string]bool
		expected      []*typesv1.LabelPair
	}{
		{
			name:          "empty attributes",
			existingAttrs: []*typesv1.LabelPair{},
			newAttrs:      []v1.KeyValue{},
			processedKeys: make(map[string]bool),
			expected:      []*typesv1.LabelPair{},
		},
		{
			name: "new unique attributes",
			existingAttrs: []*typesv1.LabelPair{
				{Name: "existing", Value: "value"},
			},
			newAttrs: []v1.KeyValue{
				{
					Key: "new",
					Value: v1.AnyValue{
						Value: &v1.AnyValue_StringValue{
							StringValue: "newvalue",
						},
					},
				},
			},
			processedKeys: map[string]bool{"existing": true},
			expected: []*typesv1.LabelPair{
				{Name: "existing", Value: "value"},
				{Name: "new", Value: "newvalue"},
			},
		},
		{
			name: "duplicate attributes",
			existingAttrs: []*typesv1.LabelPair{
				{Name: "key1", Value: "value1"},
			},
			newAttrs: []v1.KeyValue{
				{
					Key: "key1",
					Value: v1.AnyValue{
						Value: &v1.AnyValue_StringValue{
							StringValue: "value2",
						},
					},
				},
			},
			processedKeys: map[string]bool{"key1": true},
			expected: []*typesv1.LabelPair{
				{Name: "key1", Value: "value1"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appendAttributesUnique(tt.existingAttrs, tt.newAttrs, tt.processedKeys)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSymbolizedFunctionNames(t *testing.T) {
	// Create two unsymbolized locations at 0x1e0 and 0x2f0
	// Expect both of them to be present in the converted pprof
	svc := mockotlp.NewMockPushService(t)
	var profiles []*model.PushRequest
	svc.On("PushParsed", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		c := (args.Get(1)).(*model.PushRequest)
		profiles = append(profiles, c)
	}).Return(nil, nil)

	otlpb := new(otlpbuilder)
	otlpb.profile.MappingTable = []*v1experimental.Mapping{{
		MemoryStart:      0x1000,
		MemoryLimit:      0x1000,
		FilenameStrindex: otlpb.addstr("file1.so"),
	}}
	otlpb.profile.LocationTable = []*v1experimental.Location{{
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 0},
		Address:       0x1e0,
		Line:          nil,
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 0},
		Address:       0x2f0,
		Line:          nil,
	}}
	otlpb.profile.LocationIndices = []int32{0, 1}
	otlpb.profile.Sample = []*v1experimental.Sample{{
		LocationsStartIndex: 0,
		LocationsLength:     2,
		Value:               []int64{0xef},
	}}
	req := &v1experimental2.ExportProfilesServiceRequest{
		ResourceProfiles: []*v1experimental.ResourceProfiles{{
			ScopeProfiles: []*v1experimental.ScopeProfiles{{
				Profiles: []*v1experimental.Profile{
					&otlpb.profile,
				}}}}}}
	logger := testutil.NewLogger(t)
	h := NewOTLPIngestHandler(svc, logger, false)
	_, err := h.Export(context.Background(), req)
	assert.NoError(t, err)
	require.Equal(t, 1, len(profiles))

	gp := profiles[0].Series[0].Samples[0].Profile.Profile

	ss := bench.StackCollapseProtoWithOptions(gp, bench.StackCollapseOptions{
		ValueIdx:   0,
		Scale:      1,
		WithLabels: true,
	})
	require.Equal(t, 1, len(ss))
	require.Equal(t, " ||| file1.so 0x2f0;file1.so 0x1e0 239", ss[0])
}

func TestSampleAttributes(t *testing.T) {
	// Create a profile with two samples, with different sample attributes
	// one process=firefox, the other process=chrome
	// expect both of them to be present in the converted pprof as labels, but not series labels
	svc := mockotlp.NewMockPushService(t)
	var profiles []*model.PushRequest
	svc.On("PushParsed", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		c := (args.Get(1)).(*model.PushRequest)
		profiles = append(profiles, c)
	}).Return(nil, nil)

	otlpb := new(otlpbuilder)
	otlpb.profile.MappingTable = []*v1experimental.Mapping{{
		MemoryStart:      0x1000,
		MemoryLimit:      0x1000,
		FilenameStrindex: otlpb.addstr("firefox.so"),
	}, {
		MemoryStart:      0x1000,
		MemoryLimit:      0x1000,
		FilenameStrindex: otlpb.addstr("chrome.so"),
	}}

	otlpb.profile.LocationTable = []*v1experimental.Location{{
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 0},
		Address:       0x1e,
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 0},
		Address:       0x2e,
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 1},
		Address:       0x3e,
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 1},
		Address:       0x4e,
	}}
	otlpb.profile.LocationIndices = []int32{0, 1, 2, 3}
	otlpb.profile.Sample = []*v1experimental.Sample{{
		LocationsStartIndex: 0,
		LocationsLength:     2,
		Value:               []int64{0xef},
		AttributeIndices:    []int32{0},
	}, {
		LocationsStartIndex: 2,
		LocationsLength:     2,
		Value:               []int64{0xefef},
		AttributeIndices:    []int32{1},
	}}
	otlpb.profile.AttributeTable = []v1.KeyValue{{
		Key: "process",
		Value: v1.AnyValue{
			Value: &v1.AnyValue_StringValue{
				StringValue: "firefox",
			},
		},
	}, {
		Key: "process",
		Value: v1.AnyValue{
			Value: &v1.AnyValue_StringValue{
				StringValue: "chrome",
			},
		},
	}}
	req := &v1experimental2.ExportProfilesServiceRequest{
		ResourceProfiles: []*v1experimental.ResourceProfiles{{
			ScopeProfiles: []*v1experimental.ScopeProfiles{{
				Profiles: []*v1experimental.Profile{
					&otlpb.profile,
				}}}}}}
	logger := testutil.NewLogger(t)
	h := NewOTLPIngestHandler(svc, logger, false)
	_, err := h.Export(context.Background(), req)
	assert.NoError(t, err)
	require.Equal(t, 1, len(profiles))
	require.Equal(t, 1, len(profiles[0].Series))
	require.Equal(t, 1, len(profiles[0].Series[0].Samples))

	seriesLabelsMap := make(map[string]string)
	for _, label := range profiles[0].Series[0].Labels {
		seriesLabelsMap[label.Name] = label.Value
	}
	assert.Equal(t, "", seriesLabelsMap["process"])
	assert.NotContains(t, seriesLabelsMap, "service.name")

	gp := profiles[0].Series[0].Samples[0].Profile.Profile

	ss := bench.StackCollapseProtoWithOptions(gp, bench.StackCollapseOptions{
		ValueIdx:   0,
		Scale:      1,
		WithLabels: true,
	})
	fmt.Printf("%s \n", strings.Join(ss, "\n"))
	require.Equal(t, 2, len(ss))
	assert.Equal(t, "(process = chrome) ||| chrome.so 0x4e;chrome.so 0x3e 61423", ss[0])
	assert.Equal(t, "(process = firefox) ||| firefox.so 0x2e;firefox.so 0x1e 239", ss[1])
}

func TestDifferentServiceNames(t *testing.T) {
	// Create a profile with two samples having different service.name attributes
	// Expect them to be pushed as separate profiles
	svc := mockotlp.NewMockPushService(t)
	var profiles []*model.PushRequest
	svc.On("PushParsed", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		c := (args.Get(1)).(*model.PushRequest)
		profiles = append(profiles, c)
	}).Return(nil, nil)

	otlpb := new(otlpbuilder)
	otlpb.profile.MappingTable = []*v1experimental.Mapping{{
		MemoryStart:      0x1000,
		MemoryLimit:      0x2000,
		FilenameStrindex: otlpb.addstr("service-a.so"),
	}, {
		MemoryStart:      0x2000,
		MemoryLimit:      0x3000,
		FilenameStrindex: otlpb.addstr("service-b.so"),
	}, {
		MemoryStart:      0x4000,
		MemoryLimit:      0x5000,
		FilenameStrindex: otlpb.addstr("service-c.so"),
	}}

	otlpb.profile.LocationTable = []*v1experimental.Location{{
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 0}, // service-a.so
		Address:       0x1100,
		Line: []*v1experimental.Line{{
			FunctionIndex: 0,
			Line:          10,
		}},
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 0}, // service-a.so
		Address:       0x1200,
		Line: []*v1experimental.Line{{
			FunctionIndex: 1,
			Line:          20,
		}},
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 1}, // service-b.so
		Address:       0x2100,
		Line: []*v1experimental.Line{{
			FunctionIndex: 2,
			Line:          30,
		}},
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 1}, // service-b.so
		Address:       0x2200,
		Line: []*v1experimental.Line{{
			FunctionIndex: 3,
			Line:          40,
		}},
	}, {
		MappingIndex_: &v1experimental.Location_MappingIndex{MappingIndex: 2}, // service-c.so
		Address:       0xef0,
		Line: []*v1experimental.Line{{
			FunctionIndex: 4,
			Line:          50,
		}},
	}}

	otlpb.profile.FunctionTable = []*v1experimental.Function{{
		NameStrindex:       otlpb.addstr("serviceA_func1"),
		SystemNameStrindex: otlpb.addstr("serviceA_func1"),
		FilenameStrindex:   otlpb.addstr("service_a.go"),
	}, {
		NameStrindex:       otlpb.addstr("serviceA_func2"),
		SystemNameStrindex: otlpb.addstr("serviceA_func2"),
		FilenameStrindex:   otlpb.addstr("service_a.go"),
	}, {
		NameStrindex:       otlpb.addstr("serviceB_func1"),
		SystemNameStrindex: otlpb.addstr("serviceB_func1"),
		FilenameStrindex:   otlpb.addstr("service_b.go"),
	}, {
		NameStrindex:       otlpb.addstr("serviceB_func2"),
		SystemNameStrindex: otlpb.addstr("serviceB_func2"),
		FilenameStrindex:   otlpb.addstr("service_b.go"),
	}, {
		NameStrindex:       otlpb.addstr("serviceC_func3"),
		SystemNameStrindex: otlpb.addstr("serviceC_func3"),
		FilenameStrindex:   otlpb.addstr("service_c.go"),
	}}

	otlpb.profile.LocationIndices = []int32{0, 1, 2, 3, 4, 4}

	otlpb.profile.Sample = []*v1experimental.Sample{{
		LocationsStartIndex: 0,
		LocationsLength:     2, // Use first two locations
		Value:               []int64{100},
		AttributeIndices:    []int32{0},
	}, {
		LocationsStartIndex: 2,
		LocationsLength:     2,
		Value:               []int64{200},
		AttributeIndices:    []int32{1},
	}, {
		LocationsStartIndex: 4,
		LocationsLength:     2,
		Value:               []int64{700},
		AttributeIndices:    []int32{},
	}}

	otlpb.profile.AttributeTable = []v1.KeyValue{{
		Key: "service.name",
		Value: v1.AnyValue{
			Value: &v1.AnyValue_StringValue{
				StringValue: "service-a",
			},
		},
	}, {
		Key: "service.name",
		Value: v1.AnyValue{
			Value: &v1.AnyValue_StringValue{
				StringValue: "service-b",
			},
		},
	}}

	otlpb.profile.SampleType = []*v1experimental.ValueType{{
		TypeStrindex: otlpb.addstr("samples"),
		UnitStrindex: otlpb.addstr("count"),
	}}
	otlpb.profile.PeriodType = &v1experimental.ValueType{
		TypeStrindex: otlpb.addstr("cpu"),
		UnitStrindex: otlpb.addstr("nanoseconds"),
	}
	otlpb.profile.Period = 10000000 // 10ms

	req := &v1experimental2.ExportProfilesServiceRequest{
		ResourceProfiles: []*v1experimental.ResourceProfiles{{
			ScopeProfiles: []*v1experimental.ScopeProfiles{{
				Profiles: []*v1experimental.Profile{
					&otlpb.profile,
				}}}}}}

	logger := testutil.NewLogger(t)
	h := NewOTLPIngestHandler(svc, logger, false)
	_, err := h.Export(context.Background(), req)
	require.NoError(t, err)

	require.Equal(t, 3, len(profiles))

	expectedStacks := map[string]string{
		"service-a": " ||| serviceA_func2;serviceA_func1 1000000000",
		"service-b": " ||| serviceB_func2;serviceB_func1 2000000000",
		"unknown":   " ||| serviceC_func3;serviceC_func3 7000000000",
	}

	for _, p := range profiles {
		require.Equal(t, 1, len(p.Series))
		seriesLabelsMap := make(map[string]string)
		for _, label := range p.Series[0].Labels {
			seriesLabelsMap[label.Name] = label.Value
		}

		serviceName := seriesLabelsMap["service_name"]
		require.Contains(t, []string{"service-a", "service-b", "unknown"}, serviceName)
		assert.NotContains(t, seriesLabelsMap, "service.name")

		gp := p.Series[0].Samples[0].Profile.Profile

		require.Equal(t, 1, len(gp.SampleType))
		assert.Equal(t, "cpu", gp.StringTable[gp.SampleType[0].Type])
		assert.Equal(t, "nanoseconds", gp.StringTable[gp.SampleType[0].Unit])

		require.NotNil(t, gp.PeriodType)
		assert.Equal(t, "cpu", gp.StringTable[gp.PeriodType.Type])
		assert.Equal(t, "nanoseconds", gp.StringTable[gp.PeriodType.Unit])
		assert.Equal(t, int64(10000000), gp.Period)

		ss := bench.StackCollapseProtoWithOptions(gp, bench.StackCollapseOptions{
			ValueIdx:   0,
			Scale:      1,
			WithLabels: true,
		})
		require.Equal(t, 1, len(ss))
		assert.Equal(t, expectedStacks[serviceName], ss[0])
		assert.NotContains(t, ss[0], "service.name")
	}
}

type otlpbuilder struct {
	profile   v1experimental.Profile
	stringmap map[string]int32
}

func (o *otlpbuilder) addstr(s string) int32 {
	if o.stringmap == nil {
		o.stringmap = make(map[string]int32)
	}
	if idx, ok := o.stringmap[s]; ok {
		return idx
	}
	idx := int32(len(o.stringmap))
	o.stringmap[s] = idx
	o.profile.StringTable = append(o.profile.StringTable, s)
	return idx
}
