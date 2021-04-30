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

var (
	app = kingpin.New("metrics parser", "parse metrics from json and yaml")
	inputDir = app.Flag("dir", "input dir path").Required().String()
	outputFile = app.Flag("out", "metrics output file").Default("metrics_out.json").String()

	dash = app.Command("dash", "parse json dashboards in dir")
	rules = app.Command("rules", "parse yaml rules config in dir")
)

type MetricsDir struct {
	MetricsFiles	[]MetricsFile	`json:"metricsfiles"`
}

type MetricsFile struct {
	Filename       string   `json:"filename"`
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

func NewMetricsFile(fn string, metrics map[string]struct{}, errs []error) MetricsFile {

	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	errStrings := make([]string, 0, len(errs))
	for _, err := range errs {
		errStrings  = append(errStrings, err.Error())
	}

	metricsFile := MetricsFile{
		Filename: fn,
		Metrics: keys,
		ParseErrors: errStrings,
	}

	return metricsFile
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

func ParseDashboard(file *os.File) MetricsFile {

	queries := make([]string, 0)
	metrics := map[string]struct{}{}
	errors := make([]error, 0)
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
			errors = append(errors, err)
		}
	}

	metricsFile := NewMetricsFile(file.Name(), metrics, errors)
	return metricsFile

}

// todo: pull out defined rules separately
func ParseRules(file *os.File) MetricsFile {

	metrics := map[string]struct{}{}
	errors := make([]error, 0)

	var conf RuleConfig

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
				errors = append(errors, err)
			}
		}
	}

	metricsFile := NewMetricsFile(file.Name(), metrics, errors)

	return metricsFile
}

func ParseDir(dir string, isRules bool) MetricsDir {

	files, err := ioutil.ReadDir(dir)
	if err != nil {
		log.Fatalln(err)
	}

	metricsDir := MetricsDir{
		MetricsFiles: make([]MetricsFile, 0),
	}

	for _, fileInfo := range files {
		fmt.Println("Parsing: ", fileInfo.Name())
		file, err := os.Open(dir + "/" + fileInfo.Name())
		if err != nil {
			log.Fatalln(err)
		}

		if isRules {
			metricsDir.MetricsFiles = append(metricsDir.MetricsFiles, ParseRules(file))
		} else {
			metricsDir.MetricsFiles = append(metricsDir.MetricsFiles, ParseDashboard(file))
		}

		err = file.Close()
		if err != nil {
			log.Fatalln(err)
		}
	}

	return metricsDir

}

func main() {

	output := MetricsDir{}

	switch kingpin.MustParse(app.Parse(os.Args[1:])) {

	case dash.FullCommand():
		output = ParseDir(*inputDir, false)

	case rules.FullCommand():
		output = ParseDir(*inputDir, true)
	}

	out, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Fatalln(err)
	}

	if err := ioutil.WriteFile(*outputFile, out, os.FileMode(int(0666))); err != nil {
		log.Fatalln(err)
	}

}
