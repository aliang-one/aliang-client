package logger

import (
	"fmt"
	"log"
	"os"
	"reflect"
	"strconv"
	"strings"
)

// SafeSprint formats log arguments without letting a bad Error/String method
// crash the caller. It preserves fmt.Sprint spacing rules for string operands.
func SafeSprint(v ...interface{}) string {
	if len(v) == 0 {
		return ""
	}

	parts := make([]string, len(v))
	isString := make([]bool, len(v))
	for i, arg := range v {
		if s, ok := arg.(string); ok {
			parts[i] = s
			isString[i] = true
			continue
		}
		parts[i] = SafeValueString(arg)
	}

	var b strings.Builder
	for i, part := range parts {
		if i > 0 && !(isString[i-1] || isString[i]) {
			b.WriteByte(' ')
		}
		b.WriteString(part)
	}

	return b.String()
}

// SafeValueString returns fmt.Sprint(v), but degrades to a stable placeholder
// if formatting v itself panics.
func SafeValueString(v interface{}) (result string) {
	defer func() {
		if r := recover(); r != nil {
			result = fmt.Sprintf(
				"<panic while formatting %s: %s>",
				safeTypeString(v),
				SafeRecoveredValueString(r),
			)
		}
	}()

	return fmt.Sprint(v)
}

// SafeRecoveredValueString formats a recovered panic value without invoking
// Error/String methods on arbitrary types. This is intended for recover paths
// where the panic payload itself may be corrupted.
func SafeRecoveredValueString(v interface{}) string {
	if basic, ok := safeBasicValueString(v); ok {
		return basic
	}

	if v == nil {
		return "<nil>"
	}

	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer:
		if rv.IsNil() {
			return safeTypeString(v) + "(<nil>)"
		}
		return fmt.Sprintf("%s@0x%x", safeTypeString(v), rv.Pointer())
	case reflect.Slice:
		if rv.IsNil() {
			return safeTypeString(v) + "(<nil>)"
		}
		return fmt.Sprintf("%s(len=%d)", safeTypeString(v), rv.Len())
	case reflect.Array, reflect.Map, reflect.Chan, reflect.String:
		return fmt.Sprintf("%s(len=%d)", safeTypeString(v), rv.Len())
	case reflect.Func:
		if rv.IsNil() {
			return safeTypeString(v) + "(<nil>)"
		}
		return fmt.Sprintf("%s@0x%x", safeTypeString(v), rv.Pointer())
	default:
		return safeTypeString(v)
	}
}

func safeTypeString(v interface{}) string {
	if v == nil {
		return "<nil>"
	}
	t := reflect.TypeOf(v)
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

func safeBasicValueString(v interface{}) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "<nil>", true
	case string:
		return x, true
	case []byte:
		return string(x), true
	case bool:
		return strconv.FormatBool(x), true
	case int:
		return strconv.Itoa(x), true
	case int8:
		return strconv.FormatInt(int64(x), 10), true
	case int16:
		return strconv.FormatInt(int64(x), 10), true
	case int32:
		return strconv.FormatInt(int64(x), 10), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case uint:
		return strconv.FormatUint(uint64(x), 10), true
	case uint8:
		return strconv.FormatUint(uint64(x), 10), true
	case uint16:
		return strconv.FormatUint(uint64(x), 10), true
	case uint32:
		return strconv.FormatUint(uint64(x), 10), true
	case uint64:
		return strconv.FormatUint(x, 10), true
	case uintptr:
		return strconv.FormatUint(uint64(x), 10), true
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32), true
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64), true
	case complex64:
		return strconv.FormatComplex(complex128(x), 'g', -1, 64), true
	case complex128:
		return strconv.FormatComplex(x, 'g', -1, 128), true
	default:
		return "", false
	}
}

func safeLoggerOutput(l *log.Logger, calldepth int, message string) {
	defer func() {
		if r := recover(); r != nil {
			_, _ = fmt.Fprintf(
				os.Stderr,
				"logger output panic: %s; message=%q\n",
				SafeRecoveredValueString(r),
				message,
			)
		}
	}()

	if err := l.Output(calldepth, message); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "logger output error: %v; message=%q\n", err, message)
	}
}
