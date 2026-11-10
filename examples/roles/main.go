// 示例：角色解析与跨平台映射（element 包）
//
// element 包提供一套“中立角色”（如 button、link、text_field）和“原生词
// 汇前缀”（ax / atspi / uia）。一条配置可以混合中立角色与原生角色，
// 再由 ResolveRoleEntries 针对某个平台翻译成该平台的原生角色名列表。
//
// 运行：
//
//	go run ./examples/roles
package main

import (
	"fmt"

	"github.com/LouYuanbo1/neru-core/element"
)

func main() {
	// 跨平台配置示例：可以混写中立角色与带词汇前缀的原生角色。
	res := element.ResolveRoleEntries([]string{
		"button",        // 中立角色
		"link",          // 中立角色
		"ax:AXTextArea", // 指定 macOS 原生角色
		"atspi:entry",   // 指定 Linux 原生角色
		"uia:Edit",      // 指定 Windows 原生角色
		"btton",         // 故意拼错 → 致命诊断
		"uia:Unknown",   // Windows 才有的原生角色，在别的平台解析时被忽略
	}, "darwin") // 逐个平台解析；这里以 macOS 为例

	// 查看翻译结果：array 已去重、按条目顺序合并。
	fmt.Printf("在 %s 上得到的原生角色集合: %v\n", res.GOOS, res.Native)

	// 查看每个条目的解析与诊断信息。
	for _, entry := range res.Entries {
		fmt.Printf("  条目 %-14s → 原生 %v\n", entry.Entry, entry.Native)
	}
	for _, d := range res.Diagnostics {
		kind := "提示"
		if d.IsFatal() {
			kind = "致命"
		}
		fmt.Printf("  [%s] %s\n", kind, d.Message())
	}

	// 读取当前平台默认的可点击原生角色。
	if vocab, ok := element.CurrentVocabulary(); ok {
		fmt.Printf(
			"当前平台(%v)默认可点击角色: %v\n",
			vocab,
			element.DefaultClickableNativeRoles(vocab),
		)
	}

	// 遍历中立角色词汇表，看看每个角色在各平台展开成什么。
	for _, m := range element.RoleVocabulary {
		if len(m.AX) > 0 {
			fmt.Printf("  角色 %-16s ax=%v\n", m.Role, m.AX)
		}
	}
}
