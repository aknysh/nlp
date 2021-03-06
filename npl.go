// Package nlp provides general purpose Natural Language Processing.
package nlp

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"

	"github.com/cdipaolo/goml/base"
	"github.com/cdipaolo/goml/text"
)

// NL is a Natural Language Processor
type NL struct {
	models []*model
	naive  *text.NaiveBayes
	output io.Writer
}

// New returns a *NL
func New(output ...io.Writer) *NL {
	if output != nil && len(output) > 0 {
		return &NL{output: output[0]}
	}
	return &NL{output: os.Stdout}
}

// P proccesses the expr and returns one of
// the types passed as the i parameter to the RegistryModel
// func filled with the data inside expr
func (nl *NL) P(expr string) interface{} { return nl.models[nl.naive.Predict(expr)].fit(expr) }

// Learn maps the models samples to the models themselves and
// returns an error if something occurred while learning
func (nl *NL) Learn() error {
	if len(nl.models) > 0 {
		stream := make(chan base.TextDatapoint, 100)
		errors := make(chan error)
		for i := range nl.models {
			err := nl.models[i].learn()
			if err != nil {
				return err
			}
			for _, s := range nl.models[i].samples {
				stream <- base.TextDatapoint{
					X: s,
					Y: uint8(i),
				}
			}
		}
		nl.naive = text.NewNaiveBayes(stream, uint8(len(nl.models)), base.OnlyWordsAndNumbers)
		nl.naive.Output = nl.output
		go nl.naive.OnlineLearn(errors)
		close(stream)
		for {
			err := <-errors
			if err != nil {
				return fmt.Errorf("error occurred while learning: %s", err)
			}
			// training is done!
			break
		}
		return nil
	}
	return fmt.Errorf("register at least one model before learning")
}

type model struct {
	tpy     reflect.Type
	fields  []field
	keys    map[int][]key
	samples []string
	output  io.Writer
}

type field struct {
	i int                 // index
	n string              // name
	f reflect.StructField // struct field
	k reflect.Kind        // kind
}

type key struct {
	left, word, right string
	sample, field     int
}

// RegisterModel registers a model i and creates possible patterns
// from samples.
//
// NOTE: samples must have a special formatting, see example below.
//
func (nl *NL) RegisterModel(i interface{}, samples []string) error {
	if i == nil {
		return fmt.Errorf("can't create model from nil value")
	}
	if len(samples) == 0 || samples == nil {
		return fmt.Errorf("samples can't be nil or empty")
	}
	tpy, val := reflect.TypeOf(i), reflect.ValueOf(i)
	if tpy.Kind() == reflect.Struct {
		mod := &model{
			tpy:     tpy,
			samples: samples,
			output:  nl.output,
			keys:    make(map[int][]key),
		}
	NextField:
		for i := 0; i < tpy.NumField(); i++ {
			if tpy.Field(i).Anonymous || tpy.Field(i).PkgPath != "" {
				continue NextField
			}
			switch val.Field(i).Kind() {
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.String:
				mod.fields = append(mod.fields, field{i, tpy.Field(i).Name, tpy.Field(i), val.Field(i).Kind()})
			}
		}
		nl.models = append(nl.models, mod)
		return nil
	}
	return fmt.Errorf("can't create model from non-struct type")
}

func (m *model) learn() error {
	isKeyword := func(word string) bool {
		return string(word[0]) == "{" && string(word[len(word)-1]) == "}"
	}
	for sid, s := range m.samples {
		words := strings.Split(s, " ")
		wl := len(words)
		for i, word := range words {
			if isKeyword(word) {
				keyword := word[1 : len(word)-1]
				k := key{}
				kl := len(m.keys[sid])
				for fid, f := range m.fields {
					if f.n == keyword {
						if i == 0 { // {} <- first
							if i == wl-1 { // {}
								k = key{left: "", word: keyword, right: "", sample: sid, field: fid}
							} else { // {} ...
								if isKeyword(words[i+1]) { // {X} {Y} <- referring to X
									k = key{left: "", word: keyword, right: "-", sample: sid, field: fid}
								} else { // {} ...
									k = key{left: "", word: keyword, right: words[i+1], sample: sid, field: fid}
								}
							}
						} else {
							if i == wl-1 { // ... {}
								if isKeyword(words[i-1]) { // {X} {Y} <- referring to Y
									k = key{left: "-", word: keyword, right: "", sample: sid, field: fid}
								} else { // ... {}
									k = key{left: words[i-1], word: keyword, right: "", sample: sid, field: fid}
								}
							} else { // ... {} ...
								if isKeyword(words[i-1]) { // ... {X} {Y} ... <- referring to Y
									k = key{left: "-", word: keyword, right: words[i+1], sample: sid, field: fid}
								} else if isKeyword(words[i+1]) { // ... {X} {Y} ... <- referring to X
									k = key{left: words[i-1], word: keyword, right: "-", sample: sid, field: fid}
								} else { // ... {} ...
									k = key{left: words[i-1], word: keyword, right: words[i+1], sample: sid, field: fid}
								}
							}
						}
					}
				}
				m.keys[sid] = append(m.keys[sid], k)
				if len(m.keys[sid]) == kl {
					return fmt.Errorf("error while processing model samples, miss-spelled '%s'", keyword)
				}
			}
		}
	}
	return nil
}

func (m *model) selectBestSample(expr string) (int, map[string][]int) {
	scores := make(map[int]int)
	wmap := make(map[int]map[string][]int)
	words := strings.Split(expr, " ")
	wl := len(words)
	for sid, keys := range m.keys {
		for _, key := range keys {
			for i, word := range words {
				if i == 0 { // {} ...
					if i == wl-1 { // {}
						scores[sid]++
					} else { // {} ...
						if words[i+1] == key.right { // {} x -> x == key.right
							scores[sid]++
							wi := strings.Index(expr, word)
							if wmap[sid] == nil {
								wmap[sid] = make(map[string][]int)
							}
							wmap[sid][key.word] = append(wmap[sid][key.word], wi, wi+len(word))
						}
					}
				} else { // ... {} ... || ... {}
					if i == wl-1 { // ... {}
						if words[i-1] == key.left {
							scores[sid]++
							wi := strings.Index(expr, word)
							if wmap[sid] == nil {
								wmap[sid] = make(map[string][]int)
							}
							wmap[sid][key.word] = append(wmap[sid][key.word], wi, len(expr))
						}
					} else { /// ... {} ...
						if words[i-1] == key.left { // ... x {} ... -> x == key.left
							scores[sid]++
							wi := strings.Index(expr, word)
							if wmap[sid] == nil {
								wmap[sid] = make(map[string][]int)
							}
							wmap[sid][key.word] = append(wmap[sid][key.word], wi)

							lw := len(wmap[sid][key.word])
							for j := i; j < wl; j++ {
								if words[j] == key.right {
									wmap[sid][key.word] = append(wmap[sid][key.word], strings.Index(expr, words[j])-1)
								}
							}
							if reflect.New(m.tpy).Elem().Field(key.field).Kind() == reflect.String {
								if lw == len(wmap[sid][key.word]) {
									wmap[sid][key.word] = append(wmap[sid][key.word], len(expr))
								}
							} else {
								wmap[sid][key.word] = append(wmap[sid][key.word], wmap[sid][key.word][0]+len(word))
							}
						}
						if words[i+1] == key.right { // ... {} x ... -> x == key.right
							scores[sid]++
						}
					}
				}
			}
		}
	}
	bestScore := 0
	bestSampleID := -1
	for sid, score := range scores {
		if score > bestScore {
			bestScore = score
			bestSampleID = sid
		}
	}
	// fmt.Fprintf(m.output, "%#v\n\n", wmap[bestSampleID])
	return bestSampleID, wmap[bestSampleID]
}

func (m *model) fit(expr string) interface{} {
	val := reflect.New(m.tpy).Elem()
	sid, mapping := m.selectBestSample(expr)
	if sid != -1 {
		for _, key := range m.keys[sid] {
			if indices, ok := mapping[key.word]; ok {
				switch val.Field(key.field).Kind() {
				case reflect.String:
					val.Field(key.field).SetString(string(expr[indices[0]:indices[1]]))
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					s := string(expr[indices[0]:indices[1]])
					v, _ := strconv.ParseInt(s, 10, 0)
					val.Field(key.field).SetInt(v)
				}
			}
		}
		// fmt.Fprintf(m.output, "Predicted sample %#v for %#v\n", m.samples[sid], expr)
	}
	return val.Interface()
}

// Classifier is a text classifier
type Classifier struct {
	naive   *text.NaiveBayes
	classes []*class
	output  io.Writer
}

type class struct {
	name    string
	samples []string
}

// NewClassifier returns a new classifier
func NewClassifier(w ...io.Writer) *Classifier {
	if w != nil && len(w) > 0 {
		return &Classifier{output: w[0]}
	}
	return &Classifier{output: os.Stdout}
}

// NewClass creates a classification class
func (cls Classifier) NewClass(name string, samples []string) error {
	if name == "" {
		return fmt.Errorf("class name can't be empty")
	}
	if len(samples) == 0 || samples == nil {
		return fmt.Errorf("samples can't be nil or empty")
	}
	cls.classes = append(cls.classes, &class{name: name, samples: samples})
	return nil
}

// Learn is the ml process for classification
func (cls Classifier) Learn() error {
	if len(cls.classes) > 0 {
		stream := make(chan base.TextDatapoint, 100)
		errors := make(chan error)
		for i := range cls.classes {
			for _, s := range cls.classes[i].samples {
				stream <- base.TextDatapoint{
					X: s,
					Y: uint8(i),
				}
			}
		}
		cls.naive = text.NewNaiveBayes(stream, uint8(len(cls.classes)), base.OnlyWordsAndNumbers)
		cls.naive.Output = cls.output
		go cls.naive.OnlineLearn(errors)
		close(stream)
		for {
			err := <-errors
			if err != nil {
				return fmt.Errorf("error occurred while learning: %s", err)
			}
			// training is done!
			break
		}
		return nil
	}
	return fmt.Errorf("register at least one class before learning")
}

// Classify classifies expr and returns the class name
// which expr belongs to
func (cls Classifier) Classify(expr string) string { return cls.classes[cls.naive.Predict(expr)].name }
