package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"regexp"
	"io/ioutil"
	"sort"

	"github.com/pkg/errors"
	"github.com/prometheus/prometheus/promql/parser"
	log "github.com/sirupsen/logrus"
	"github.com/itchyny/gojq"
	"gopkg.in/yaml.v2"
	"gopkg.in/alecthomas/kingpin.v2"
)

// todo: files
var (
	app = kingpin.New("metrics parser", "parse metrics from json and yaml")
	inputDir = app.Flag("dir", "input dir path").Required().String()
	outputFile = app.Flag("out", "metrics output file").Default("metrics_out.json").String()

	dash = app.Command("dash", "parse json dashboards in dir")
	rules = app.Command("rules", "parse yaml rules config in dir")
)

type Metrics struct {
	Metrics        []string	`json:"metrics"`
	ParseErrors    []string `json:"parse_errors"`
}

// todo: dashboard structs https://github.com/grafana-tools/sdk/issues/130#issuecomment-797018658
type RuleConfig struct {
        RuleGroups	[]RuleGroup	`yaml:"groups"`
}

type RuleGroup struct {
	Name     string     `yaml:"name"`
	Rules    []Rule     `yaml:"rules"`
}

type Rule struct {
	Record      string            `yaml:"record,omitempty"`
	Alert       string            `yaml:"alert,omitempty"`
	Expr        string            `yaml:"expr"`
	Labels      map[string]string `yaml:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty"`
}

func ParseQuery(query string, metrics map[string]struct{}) error {

	var parseError error

	query = strings.ReplaceAll(query, `\"`, `"`)
	query = strings.ReplaceAll(query, `\n`, ``)
	query = strings.ReplaceAll(query, `$__interval`, "5m")
	query = strings.ReplaceAll(query, `$__rate_interval`, "5m")
	query = strings.ReplaceAll(query, `$interval`, "5m")
	query = strings.ReplaceAll(query, `$resolution`, "5s")

	// label_values 
	if strings.Contains(query, "label_values"){
		re := regexp.MustCompile(`label_values\(([a-zA-Z0-9_]+)`)
		query = re.FindStringSubmatch(query)[1]
	}

	expr, err := parser.ParseExpr(query)
	if err != nil {
		parseError = errors.Wrapf(err, "promql query=%v", query)
		log.Debugln("msg", "promql parse error", "err", err, "query", query)
		return parseError
	}

	parser.Inspect(expr, func(node parser.Node, path []parser.Node) error {
		if n, ok := node.(*parser.VectorSelector); ok {
			metrics[n.Name] = struct{}{}
		}
		return nil
	})

	return nil
}

func ParseJq(queries *[]string, jsonData map[string]interface{}, jqExpr string) error {

	query, err := gojq.Parse(jqExpr)
	if err != nil {
		return err
	}

	iter := query.Run(jsonData)
	for {
		v, ok := iter.Next()
		if !ok {
			break
		}
		if err, ok := v.(error); ok {
			return err
		}
		*queries = append(*queries, v.(string))
	}

	return nil
}

func ParseDashboard(file *os.File, metrics map[string]struct{}) []error {

	queries := make([]string, 0)
	parseErrors := make([]error, 0)
	res := map[string]interface{}{}

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalln(err)
	}

	json.Unmarshal([]byte(bytes), &res)
	err = ParseJq(&queries, res, ".templating.list[].query")
	if err != nil {
		log.Fatalln(err)
	}

	err = ParseJq(&queries, res, ".panels[]?.targets[]?.expr")
	if err != nil {
		log.Fatalln(err)
	}

	err =  ParseJq(&queries, res, ".rows[].panels[].targets[].expr")
	if err != nil {
		log.Fatalln(err)
	}

	for _, query := range queries {
		err := ParseQuery(query, metrics)
		if err != nil {
			err = errors.Wrapf(err, "file=%v", file.Name())
			parseErrors = append(parseErrors, err)
		}
	}

	return parseErrors

}

// todo: pull out defined rules separately
func ParseRules(file *os.File, metrics map[string]struct{}) []error {

	var conf RuleConfig
	parseErrors := make([]error, 0)

	rulesFile, err := ioutil.ReadAll(file)
	if err != nil {
		log.Fatalln(err)
	}

	err = yaml.Unmarshal(rulesFile, &conf)
	if err != nil {
		log.Fatalln(err)
	}

	groups := conf.RuleGroups
	for _, group := range groups {
		for _, rule := range group.Rules {
			err := ParseQuery(rule.Expr, metrics)
			if err != nil {
				parseErrors = append(parseErrors, err)
			}
		}
	}

	return parseErrors
}

func ParseDir(dir string, metrics map[string]struct{}, isRules bool) []error {

	files, err := ioutil.ReadDir(dir)
	if err != nil {
		log.Fatalln(err)
	}

	errors := make([]error, 0)

	for _, fileInfo := range files {
		fmt.Println("Parsing: ", fileInfo.Name())
		file, err := os.Open(dir + "/" + fileInfo.Name())
		if err != nil {
			log.Fatalln(err)
		}

		if isRules {
			errors = append(errors, ParseRules(file, metrics)...)
		} else {
			errors = append(errors, ParseDashboard(file, metrics)...)
		}

		err = file.Close()
		if err != nil {
			log.Fatalln(err)
		}
	}

	return errors

}

func main() {

	metrics := map[string]struct{}{}
	errors := make([]error, 0)

	switch kingpin.MustParse(app.Parse(os.Args[1:])) {

	case dash.FullCommand():
		errors = append(errors, ParseDir(*inputDir, metrics, false)...)

	case rules.FullCommand():
		errors = append(errors, ParseDir(*inputDir, metrics, true)...)
	}

	// is there a better way to do this (+ next 2 blocks)
	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	errStrings := make([]string, 0, len(errors))
	for _, err := range errors{
		errStrings  = append(errStrings, err.Error())
	}

	output := Metrics{
		Metrics: keys,
		ParseErrors: errStrings,
	}

	out, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Fatalln(err)
	}

	if err := ioutil.WriteFile(*outputFile, out, os.FileMode(int(0666))); err != nil {
		log.Fatalln(err)
	}

}
