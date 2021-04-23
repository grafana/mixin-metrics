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

// todo.. prob just operate on files
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
	PromQlErrors   []string `json:"promql_errors"`
}

// todo...structs for dashboards
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

	return parseError

}

func ParseQueries(dirtyQueries []string)  (map[string]struct{}, []error) {

	metrics := map[string]struct{}{}
	parseErrors := []error{}

	for _, query := range dirtyQueries {
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

		err := ParseQuery(query, metrics)
		if err != nil {
			parseErrors = append(parseErrors, err)
		}

	}

	return metrics, parseErrors
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

func ParseDashboard(file *os.File) ([]string, []error) {

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
		parseErrors = append(parseErrors, err)
	}

	err = ParseJq(&queries, res, ".panels[]?.targets[].expr")
	if err != nil {
		parseErrors = append(parseErrors, err)
	}

	err =  ParseJq(&queries, res, ".rows[].panels[].targets[].expr")
	if err != nil {
		parseErrors = append(parseErrors, err)
	}

	return queries, parseErrors

}

// todo: pull out defined rules separately
func ParseRules(file *os.File)([]string, []error){

	var conf RuleConfig
	queries := make([]string, 0)
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
			queries = append(queries, rule.Expr)
		}
	}

	return queries, parseErrors

}

func ParseDir(dir string, isRules bool) ([]string, []error) {

	files, err := ioutil.ReadDir(dir)
	if err != nil {
		log.Fatalln(err)
	}

	queries := make([]string, 0)
	errors := make([]error, 0)

	for _, fileInfo := range files {
		fmt.Println("Parsing: ", fileInfo.Name())
		file, err := os.Open(dir + "/" + fileInfo.Name())
		if err != nil {
			log.Fatalln(err)
		}

		if isRules {
			parsedQueries, errs  := ParseRules(file)
			queries = append(queries, parsedQueries...)
			errors = append(errors, errs...)
		} else {
			parsedQueries, errs := ParseDashboard(file)
			queries = append(queries, parsedQueries...)
			errors = append(errors, errs...)
		}

		err = file.Close()
		if err != nil {
			log.Fatalln(err)
		}
	}

	return queries, errors

}

func main() {

	queries := make([]string, 0)
	errors := make([]error, 0)

	switch kingpin.MustParse(app.Parse(os.Args[1:])) {

	// parse out queries using jq or yaml
	case dash.FullCommand():
		parseQueries, parseErrors := ParseDir(*inputDir, false)
		queries = append(queries, parseQueries...)
		errors = append(errors, parseErrors...)

	case rules.FullCommand():
		parseQueries, parseErrors := ParseDir(*inputDir, true)
		queries = append(queries, parseQueries...)
		errors = append(errors, parseErrors...)
	}

	// parse out metrics from queries using promql parser
	metrics, promqlParseErrors := ParseQueries(queries)

	// is there a better way to do this (+ next 2 blocks)
	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parseErrStrings := make([]string, 0, len(errors))
	for _, err := range errors{
		parseErrStrings  = append(parseErrStrings, err.Error())
	}

	promqlErrStrings := make([]string, 0, len(promqlParseErrors))
	for _, err := range promqlParseErrors{
		promqlErrStrings = append(promqlErrStrings, err.Error())
	}

	// output... todo: per dashboard
	output := Metrics{
		Metrics: keys,
		ParseErrors: parseErrStrings,
		PromQlErrors: promqlErrStrings,
	}

	out, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Fatalln(err)
	}

	if err := ioutil.WriteFile(*outputFile, out, os.FileMode(int(0666))); err != nil {
		log.Fatalln(err)
	}

}
