package rku

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"github.com/mitchellh/mapstructure"
	"github.com/rookie-ninja/rk-logger"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"gopkg.in/natefinch/lumberjack.v2"
	"io/ioutil"
	"os"
	"path"
	"reflect"
	"strconv"
	"strings"
	"sync"
)

var (
	envLogOnce  sync.Once
	flagLogOnce sync.Once
)

// UnmarshalBootYAML this function will parse boot config file with ENV and pflag overrides.
//
// User who want to implement his/her own entry, may use this function to parse YAML config into struct.
// This function would also parse --rkset flags.
//
// This function would do the following:
// 1: Read config file and unmarshal content into a map.
// 2: Read --rkset flags and override values in map unmarshalled at above step.
// 3: Unmarshal map into user provided struct.
//
//
// [Flag]: Override boot config value with flag of rkset:
//
// pflag.FlagSet which contains rkset as key.
//
// Receives flattened boot config file(YAML) keys and override them in provided boot config.
// We follow the way of HELM does while overriding keys, refer to https://helm.sh/docs/intro/using_helm/
// example:
//
// Lets assuming we have boot config YAML file as bellow:
//
// example-boot.yaml:
// gin:
//   - port: 1949
//     commonService:
//       enabled: true
//
// We can override values in example-boot.yaml file as bellow:
//
// ./your_compiled_binary --rkset "gin[0].port=2008,gin[0].commonService.enabled=false"
//
// Basic rules:
// 1: Using comma(,) to separate different k/v section.
// 2: Using [index] to access arrays in YAML file.
// 3: Using equal sign(=) to distinguish key and value.
// 4: Using dot(.) to access map in YAML file.
//
// [Environment variable]: Override boot config value
//
// Prefix of "RK" will be used as environment variable key. The schema follows above.
//
// example-boot.yaml:
// gin:
//   - port: 1949
//     commonService:
//       enabled: true
//
// We can override values in example-boot.yaml file as bellow:
//
// os.Setenv("RK_GIN_0_PORT", "2008")
// os.Setenv("RK_GIN_0_COMMONSERVICE_ENABLED", "false")
//
// ./your_compiled_binary
func UnmarshalBootYAML(raw []byte, config interface{}) {
	// 1: unmarshal original
	originalBootM := map[interface{}]interface{}{}
	vp := viper.New()
	vp.SetConfigType("yaml")
	vp.ReadConfig(bytes.NewReader(raw))
	if err := vp.Unmarshal(&originalBootM); err != nil {
		ShutdownWithError(err)
	}

	// 2: get ENV overrides
	// ignoring error, output to stdout already
	envOverridesBootM, _ := parseEnvOverrides("RK")

	// 3: get flag overrides
	pFlag := pflag.NewFlagSet("rk", pflag.ContinueOnError)
	pFlag.String("rkset", "", "")
	// ignoring error, output to stdout already
	flagOverridesBootM, _ := parseFlagOverrides(pFlag)

	// 4: override environment first, and then flags
	OverrideMap(originalBootM, envOverridesBootM)
	OverrideMap(originalBootM, flagOverridesBootM)

	// 5: unmarshal to struct
	if err := mapstructure.Decode(originalBootM, config); err != nil {
		ShutdownWithError(err)
	}
}

// parseBootOverrides parses a set line.
//
// A set line is of the form name1=value1,name2=value2
func parseBootOverrides(s string) (map[interface{}]interface{}, error) {
	vals := map[interface{}]interface{}{}
	scanner := bytes.NewBufferString(s)
	t := newParser(scanner, vals, false)
	err := t.parse()
	return vals, err
}

// reformatEnvKey will try to reformat array element
// Example:
// gin:
//   - name: greeter
//
// In order to override name, env values should be like: RK_GIN_0_NAME=greeter-replaced
//
// This function will convert RK_GIN_0_NAME to rk.gin[0].name
func reformatEnvKey(input string) string {
	list := make([]string, 0)

	tokens := strings.Split(input, ".")
	for i := range tokens {
		token := tokens[i]
		index, err := strconv.Atoi(token)
		if err != nil {
			list = append(list, token)
			continue
		}

		if i > 0 {
			listLastIndex := len(list) - 1
			if listLastIndex >= 0 {
				list[listLastIndex] = fmt.Sprintf("%s[%d]", list[listLastIndex], index)
			}
		}
	}

	return strings.Join(list, ".")
}

// parseEnvOverrides read environment variables and convert to map
func parseEnvOverrides(prefix string) (map[interface{}]interface{}, error) {
	overrideValueList := make([]string, 0)
	forLogList := make([]string, 0)

	// 1: iterate ENV values and filter with prefix
	for _, val := range os.Environ() {
		if !strings.HasPrefix(val, strings.ToUpper(prefix)+"_") {
			continue
		}

		tokens := strings.SplitN(val, "=", 2)
		if len(tokens) != 2 {
			continue
		}

		// convert key
		newKey := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(tokens[0], strings.ToUpper(prefix)+"_"), "_", "."))
		// Notice: in order to distinguish arrays in key, we defined a special case as bellow.
		// 1: Environment variables allowed is [a-zA-Z_], so we will use [_number_] to represent arrays. The downside is do not start name with number
		//
		// Example:
		// gin:
		//   - name: greeter
		//
		// In order to override name, env values should be like: RK_GIN_0_NAME=greeter-replaced
		newKey = reformatEnvKey(newKey)
		newValue := tokens[1]

		forLogList = append(forLogList, fmt.Sprintf("%s => %s=%s", val, newKey, newValue))

		overrideValueList = append(overrideValueList, fmt.Sprintf("%s=%s", newKey, newValue))
	}

	// 2: flatten values
	overrideValueFlatten := strings.Join(overrideValueList, ",")

	// 3: parse to map
	res, err := parseBootOverrides(overrideValueFlatten)

	envLogOnce.Do(func() {
		if len(forLogList) > 0 {
			zapFields := []zap.Field{
				zap.Strings("env", forLogList),
			}

			if err != nil {
				rklogger.StdoutLogger.Warn("Found ENV to override, but failed to parse, ignoring...", zapFields...)
			} else {
				rklogger.StdoutLogger.Info("Found ENV to override, applying...", zapFields...)
			}
		}
	})

	return res, err
}

// parseEnvOverrides read flag values and convert to map
func parseFlagOverrides(set *pflag.FlagSet) (map[interface{}]interface{}, error) {
	overrideValueList := make([]string, 0)

	// 1: iterate pFlag values and filter with prefix
	set.ParseAll(os.Args[1:], func(flag *pflag.Flag, value string) error {
		overrideValueList = append(overrideValueList, value)
		return nil
	})

	// 2: flatten values
	overrideValueFlatten := strings.Join(overrideValueList, ",")

	// 3: parse to map
	res, err := parseBootOverrides(overrideValueFlatten)

	flagLogOnce.Do(func() {
		if len(overrideValueFlatten) > 0 {
			zapFields := []zap.Field{
				zap.Strings("flags", overrideValueList),
			}

			if err != nil {
				rklogger.StdoutLogger.Warn("Found flag to override, but failed to parse, ignoring...", zapFields...)
			} else {
				rklogger.StdoutLogger.Info("Found flag to override, applying...", zapFields...)
			}
		}
	})

	return res, err
}

// LowerKeyInMap iterate map structure and convert string type key to lower case
func LowerKeyInMap(src map[interface{}]interface{}) map[interface{}]interface{} {
	if src == nil {
		return src
	}

	res := map[interface{}]interface{}{}

	for k, v := range src {
		keyKind := reflect.TypeOf(k).Kind()
		if keyKind == reflect.String {
			k = strings.ToLower(k.(string))
		}

		valueKind := reflect.TypeOf(v).Kind()
		switch valueKind {
		case reflect.Slice, reflect.Array:
			res[k] = LowerKeyInSlice(v.([]interface{}))
		case reflect.Map:
			res[k] = LowerKeyInMap(v.(map[interface{}]interface{}))
		default:
			res[k] = v
		}
	}

	return res
}

// LowerKeyInSlice iterate slice structure and convert string type key to lower case
func LowerKeyInSlice(src []interface{}) []interface{} {
	if src == nil {
		return src
	}

	res := make([]interface{}, 0)

	for i := range src {
		switch reflect.TypeOf(src[i]).Kind() {
		case reflect.Slice, reflect.Array:
			res = append(res, LowerKeyInSlice(src[i].([]interface{})))
		case reflect.Map:
			res = append(res, LowerKeyInMap(src[i].(map[interface{}]interface{})))
		default:
			res = append(res, src[i])
		}
	}

	return res
}

// OverrideMap override source map with new map items.
// It will iterate through all items in map and check map and slice types of item to recursively override values
//
// Mainly used for unmarshalling YAML to map.
func OverrideMap(src map[interface{}]interface{}, override map[interface{}]interface{}) {
	if src == nil || override == nil {
		return
	}

	for k, overrideItem := range override {
		originalItem, ok := src[k]
		if ok && reflect.TypeOf(originalItem) == reflect.TypeOf(overrideItem) {
			switch overrideItem.(type) {
			case []interface{}:
				OverrideSlice(originalItem.([]interface{}), overrideItem.([]interface{}))
			case map[interface{}]interface{}:
				OverrideMap(originalItem.(map[interface{}]interface{}), overrideItem.(map[interface{}]interface{}))
			default:
				src[k] = overrideItem
			}
		}
	}
}

// OverrideSlice override source slice with new slice items.
// It will iterate through all items in slice and check map and slice types of item to recursively override values
//
// Mainly used for unmarshalling YAML to map.
func OverrideSlice(src []interface{}, override []interface{}) {
	if src == nil || override == nil {
		return
	}

	for i := range override {
		if override[i] != nil && len(src)-1 >= i && reflect.TypeOf(override[i]) == reflect.TypeOf(src[i]) {
			overrideItem := override[i]
			originalItem := src[i]
			switch overrideItem.(type) {
			case []interface{}:
				OverrideSlice(originalItem.([]interface{}), overrideItem.([]interface{}))
			case map[interface{}]interface{}:
				OverrideMap(originalItem.(map[interface{}]interface{}), overrideItem.(map[interface{}]interface{}))
			default:
				src[i] = override[i]
			}
		}
	}
}

// OverrideLumberjackConfig override lumberjack config.
// This function will override fields of non-empty and non-nil.
func OverrideLumberjackConfig(origin *lumberjack.Logger, override *lumberjack.Logger) {
	if override == nil {
		return
	}
	origin.Compress = override.Compress
	origin.LocalTime = override.LocalTime
	if override.MaxAge > 0 {
		origin.MaxAge = override.MaxAge
	}

	if override.MaxBackups > 0 {
		origin.MaxBackups = override.MaxBackups
	}

	if override.MaxSize > 0 {
		origin.MaxSize = override.MaxSize
	}

	if len(override.Filename) > 0 {
		origin.Filename = override.Filename
	}
}

// OverrideZapConfig overrides zap config.
// This function will override fields of non-empty and non-nil.
func OverrideZapConfig(origin *zap.Config, override *zap.Config) {
	if override == nil {
		return
	}

	// by default, these fields would be false
	// so just override it with new config
	origin.Development = override.Development
	origin.DisableCaller = override.DisableCaller
	origin.DisableStacktrace = override.DisableStacktrace

	if len(override.Encoding) > 0 {
		origin.Encoding = override.Encoding
	}

	if !reflect.ValueOf(override.Level).Field(0).IsNil() {
		origin.Level.SetLevel(override.Level.Level())
	}

	if len(override.InitialFields) > 0 {
		origin.InitialFields = override.InitialFields
	}

	if len(override.ErrorOutputPaths) > 0 {
		origin.ErrorOutputPaths = override.ErrorOutputPaths
	}

	if len(override.OutputPaths) > 0 {
		origin.OutputPaths = override.OutputPaths
	}

	if override.Sampling != nil {
		origin.Sampling = override.Sampling
	}

	// deal with encoder config
	if len(override.EncoderConfig.CallerKey) > 0 {
		origin.EncoderConfig.CallerKey = override.EncoderConfig.CallerKey
	}

	if len(override.EncoderConfig.ConsoleSeparator) > 0 {
		origin.EncoderConfig.ConsoleSeparator = override.EncoderConfig.ConsoleSeparator
	}

	if override.EncoderConfig.EncodeCaller != nil {
		origin.EncoderConfig.EncodeCaller = override.EncoderConfig.EncodeCaller
	}

	if override.EncoderConfig.EncodeDuration != nil {
		origin.EncoderConfig.EncodeDuration = override.EncoderConfig.EncodeDuration
	}

	if override.EncoderConfig.EncodeLevel != nil {
		origin.EncoderConfig.EncodeLevel = override.EncoderConfig.EncodeLevel
	}

	if override.EncoderConfig.EncodeName != nil {
		origin.EncoderConfig.EncodeName = override.EncoderConfig.EncodeName
	}

	if override.EncoderConfig.EncodeTime != nil {
		origin.EncoderConfig.EncodeTime = override.EncoderConfig.EncodeTime
	}

	if len(override.EncoderConfig.MessageKey) > 0 {
		origin.EncoderConfig.MessageKey = override.EncoderConfig.MessageKey
	}

	if len(override.EncoderConfig.LevelKey) > 0 {
		origin.EncoderConfig.LevelKey = override.EncoderConfig.LevelKey
	}

	if len(override.EncoderConfig.TimeKey) > 0 {
		origin.EncoderConfig.TimeKey = override.EncoderConfig.TimeKey
	}

	if len(override.EncoderConfig.NameKey) > 0 {
		origin.EncoderConfig.NameKey = override.EncoderConfig.NameKey
	}

	if len(override.EncoderConfig.FunctionKey) > 0 {
		origin.EncoderConfig.FunctionKey = override.EncoderConfig.FunctionKey
	}

	if len(override.EncoderConfig.StacktraceKey) > 0 {
		origin.EncoderConfig.StacktraceKey = override.EncoderConfig.StacktraceKey
	}

	if len(override.EncoderConfig.LineEnding) > 0 {
		origin.EncoderConfig.LineEnding = override.EncoderConfig.LineEnding
	}
}

// IsValidDomain mainly used in entry config.
func IsValidDomain(domain string) bool {
	if len(domain) < 1 {
		domain = "*"
	}

	domainFromEnv := GetDefaultIfEmptyString(os.Getenv("DOMAIN"), "")

	if domain != "*" && domain != domainFromEnv {
		return false
	}

	return true
}

// GetDefaultIfEmptyString returns default value if original string is empty.
func GetDefaultIfEmptyString(origin, def string) string {
	if len(origin) < 1 {
		return def
	}

	return origin
}

// ShutdownWithError shuts down and panic.
func ShutdownWithError(err error) {
	if err == nil {
		err = errors.New("internal error")
	}
	panic(err)
}

func ReadFileFromLocal(filePath string, shouldPanic bool) []byte {
	wd, _ := os.Getwd()

	if !path.IsAbs(filePath) {
		filePath = path.Join(wd, filePath)
	}

	data, err := ioutil.ReadFile(filePath)
	if err != nil && shouldPanic {
		ShutdownWithError(err)
	}

	if err != nil {
		return []byte{}
	}

	return data
}

func ReadFileFromEmbed(filePath string, fs *embed.FS, shouldPanic bool) []byte {
	data, err := fs.ReadFile(filePath)

	if err != nil && shouldPanic {
		ShutdownWithError(err)
	}

	if err != nil {
		return []byte{}
	}

	return data
}

// fileExistFromLocal checks File existence, file path should be full path.
func fileExistFromLocal(filePath string) bool {
	if file, err := os.Stat(filePath); err != nil {
		return false
	} else if file.IsDir() {
		return false
	}
	return true
}

// fileExistFromEmbed checks File existence, file path should be full path.
func fileExistFromEmbed(filePath string, fs *embed.FS) bool {
	f, err := fs.Open(filePath)
	if err != nil {
		return false
	}

	fileInfo, err := f.Stat()
	if err != nil {
		return false
	}

	if fileInfo.IsDir() {
		return false
	}

	return true
}
