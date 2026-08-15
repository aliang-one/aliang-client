package utils

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html/charset"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// convertGBKToUTF8 将 GBK 编码转换为 UTF-8
func convertGBKToUTF8(s string) (string, error) {
	reader := transform.NewReader(strings.NewReader(s), simplifiedchinese.GBK.NewDecoder())
	d, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	return string(d), nil
}

//
//// isGBK 检测字符串是否为GBK编码
//func isGBK(data []byte) bool {
//	length := len(data)
//	var i int = 0
//	for i < length {
//		if data[i] <= 0x7f {
//			// ASCII字符
//			i++
//			continue
//		} else {
//			// 非ASCII字符，检查是否是GBK编码
//			if i+1 < length {
//				// GBK编码的第一个字节范围是0x81-0xfe
//				// 第二个字节范围是0x40-0xfe
//				if data[i] >= 0x81 && data[i] <= 0xfe &&
//					data[i+1] >= 0x40 && data[i+1] <= 0xfe {
//					i += 2
//					continue
//				}
//			}
//			return false
//		}
//	}
//	return true
//}
//
//// AutoConvertEncoding 自动检测并转换编码
//func AutoConvertEncoding(data []byte) (string, error) {
//	if isGBK(data) {
//		return convertGBKToUTF8(string(data))
//	}
//	return string(data), nil
//}

// tryDecode 尝试用指定编码解码
func tryDecode(data []byte, encodingName string) (string, error) {
	enc, _ := charset.Lookup(encodingName)
	if enc == nil {
		return "", fmt.Errorf("unsupported encoding: %s", encodingName)
	}

	reader := transform.NewReader(bytes.NewReader(data), enc.NewDecoder())
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}

	return string(decoded), nil
}

// AutoConvertEncoding 尝试 UTF-8，失败则尝试 GBK
func AutoConvertEncoding(data []byte) (string, error) {
	// 尝试 UTF-8
	if utf8.Valid(data) {
		return string(data), nil
	}

	// 尝试 GBK
	str, err := tryDecode(data, "gbk")
	if err == nil {
		return str, nil
	}

	// 实在不行就返回原始
	return string(data), fmt.Errorf("无法识别编码，已原样返回")
}
