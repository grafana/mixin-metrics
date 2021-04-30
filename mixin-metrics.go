
// parses Prom metrics from dashboard JSON and YAML rules
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

// usage:
// mixin-metrics --dir=DIR (--out=OUT_DIR --print) [dash | rules] 
var (
	app = kingpin.New("prom metrics parser", "parse metrics from json and yaml")
	inputDir = app.Flag("dir", "input dir path").Required().String()
	outputFile = app.Flag("out", "metrics output file").Default("metrics_out.json").String()
	printMetrics = app.Flag("print", "print all metrics").Bool()

	dash = app.Command("dash", "parse json dashboards in dir")
	rules = app.Command("rules", "parse yaml rules config in dir")
)

type MetricsDir struct {
	MetricsFiles	[]MetricsFile	`json:"metricsfiles"`
}

type MetricsFile struct {
	Filename       string   `json:"filename"`
	// slice of prom metrics in file
	Metrics        []string	`json:"metrics"`
	// promql parse errors in file
	ParseErrors    []string `json:"parse_errors"`
}

// todo: dashboard structs https://github.com/grafana-tools/sdk/issues/130#issuecomment-797018658
// using jq for now
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

// create MetricsFile struct
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

// use promql parser on a query to extract metrics
func ParseQuery(query string, metrics map[string]struct{}) error {

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
		err = errors.Wrapf(err, "promql query=%v", query)
		log.Debugln("msg", "promql parse error", "err", err, "query", query)
		return err
	}

	parser.Inspect(expr, func(node parser.Node, path []parser.Node) error {
		if n, ok := node.(*parser.VectorSelector); ok {
			metrics[n.Name] = struct{}{}
		}
		return nil
	})

	return nil
}

// use jq on a file to extract prom queries
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

// parses through a dashboard to extract queries, and then metrics from queries
func ParseDashboard(file *os.File) (*MetricsFile, error) {

	queries := make([]string, 0)
	metrics := map[string]struct{}{}
	errors := make([]error, 0)
	res := map[string]interface{}{}

	bytes, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, err
	}

	json.Unmarshal([]byte(bytes), &res)
	if err := ParseJq(&queries, res, ".templating.list[].query"); err != nil {
		return nil, err
	}

	if err := ParseJq(&queries, res, ".panels[]?.targets[]?.expr"); err != nil {
		return nil, err
	}

	if err := ParseJq(&queries, res, ".rows[].panels[].targets[].expr"); err != nil {
		return nil, err
	}

	for _, query := range queries {
		if err := ParseQuery(query, metrics); err != nil {
			errors = append(errors, err)
		}
	}

	metricsFile := NewMetricsFile(file.Name(), metrics, errors)
	return &metricsFile, nil
}

// todo: separate rules and raw metrics
// parses through a rules file and extracts queries, and then metrics from queries
func ParseRules(file *os.File) (*MetricsFile, error) {

	metrics := map[string]struct{}{}
	errors := make([]error, 0)

	var conf RuleConfig

	rulesFile, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal(rulesFile, &conf)
	if err != nil {
		return nil, err
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

	return &metricsFile, nil
}

// iterates over all rules/dash files in dir
func ParseDir(dir string, isRules bool) (*MetricsDir, error) {

	files, err := ioutil.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	metricsDir := MetricsDir{
		MetricsFiles: make([]MetricsFile, 0),
	}

	for _, fileInfo := range files {
		fmt.Println("Parsing: ", fileInfo.Name())
		file, err := os.Open(dir + "/" + fileInfo.Name())
		if err != nil {
			return nil, err
		}

		if isRules {
			metricsFile, err := ParseRules(file)
			if err != nil {
				return nil, err
			}
			metricsDir.MetricsFiles = append(metricsDir.MetricsFiles, *metricsFile)
		} else {
			metricsFile, err := ParseDashboard(file)
			if err != nil {
				return nil, err
			}
			metricsDir.MetricsFiles = append(metricsDir.MetricsFiles, *metricsFile)
		}

		err = file.Close()
		if err != nil {
			return nil, err
		}
	}

	return &metricsDir, nil

}

// write out MetricsDir to outputFile
func (md *MetricsDir) WriteOut(outputFile string) error {
	out, err := json.MarshalIndent(*md, "", "  ")
	if err != nil {
		return err
	}

	if err := ioutil.WriteFile(outputFile, out, os.FileMode(int(0666))); err != nil {
		return err
	}

	return nil
}

// prints parsed metrics in relabel_configs form
func (md *MetricsDir) PrintMetrics() {

	metrics := map[string]struct{}{}

	for _, metricsFile := range md.MetricsFiles {
		for _, metric := range metricsFile.Metrics{
			metrics[metric] = struct{}{}
		}
	}

	keys := make([]string, 0, len(metrics))
	for k := range metrics {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Println(strings.Join(keys, " | "))
}

// todo: running `rules` on a dash dir doesn't fail? :(
func main() {

	var output *MetricsDir
	var err error

	switch kingpin.MustParse(app.Parse(os.Args[1:])) {

	case dash.FullCommand():
		output, err = ParseDir(*inputDir, false)
		if err != nil {
			log.Fatalln(err)
		}

	case rules.FullCommand():
		output, err = ParseDir(*inputDir, true)
		if err != nil {
			log.Fatalln(err)
		}
	}

	if *printMetrics {
		output.PrintMetrics()
		return
	}

	if err := output.WriteOut(*outputFile); err != nil {
		log.Fatalln(err)
	}
}
