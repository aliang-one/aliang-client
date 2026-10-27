package agentcore

import (
	"fmt"
	"strings"
)

// 历史图片占位符化。
//
// 手机上传的照片在 agentcoreMessages 里被内联成 base64 图片块挂到最后一条
// user 消息上，而这条消息会跟着 history 一路留在那里——run 内每一轮请求都
// 把它整段重发一遍。一张 5MB 照片 = 6.7MB base64，若 run 跑 20 轮，等于为同
// 一张图多推 130MB（prompt cache 也救不了带宽：缓存省的是 prefill，请求体照
// 样上传）。
//
// 语义上的取舍：模型在第一轮已经看过这张图，后续轮次它只需要知道「这里曾有
// 一张图、是什么媒介类型」，不需要再看一遍像素。所以第一轮结束后把历史里的
// 图片块换成一行文字占位符。
//
// 代价是固定的：这次替换会改写历史中段，使本轮的缓存前缀失效一次；替换之后
// 历史形状不再变化，缓存重新稳定。换掉 6.7MB 的上传量，值这一次失效。
//
// 不做的事：不动本轮新附的图（那是用户这一轮真正想给模型看的东西，替换发生
// 在「本轮请求已经发出去之后」）。
func (r *runner) stubHistoricalImages() {
	replaced := 0
	var droppedBytes int
	for i := range r.history {
		blocks, ok := r.history[i].Content.([]ContentBlock)
		if !ok {
			continue
		}
		// history 里的切片可能与上一轮发出去的请求体共享底层数组，就地改等于
		// 隔空改别人的请求。先整片复制，再只在副本上动手。
		cp := make([]ContentBlock, len(blocks))
		copy(cp, blocks)
		changed := false
		for j := range cp {
			if cp[j].Type != BlockTypeImage {
				continue
			}
			mediaType := ""
			if cp[j].Source != nil {
				droppedBytes += len(cp[j].Source.Data)
				mediaType = cp[j].Source.MediaType
			}
			cp[j] = ContentBlock{
				Type: BlockTypeText,
				Text: imagePlaceholderText(mediaType),
			}
			replaced++
			changed = true
		}
		if changed {
			r.history[i].Content = cp
		}
	}
	if replaced > 0 {
		r.logf("[agentcore] 历史图片占位符化 %d 张，释放本轮上传约 %dKB session=%s",
			replaced, droppedBytes*3/4/1024, r.sid())
	}
}

// imagePlaceholderText 是替换图片块的文字。保留媒介类型：模型据此知道那是
// 一张照片还是一条截图，回答「那张图里写了什么」时也知道该请用户重发。
func imagePlaceholderText(mediaType string) string {
	mediaType = strings.TrimSpace(mediaType)
	if mediaType == "" {
		mediaType = "image"
	}
	return fmt.Sprintf("[图片已省略：用户早前发送的一张 %s，内容在你上一轮的上下文中已处理过。若还需要看清细节，请让用户重新发送。]", mediaType)
}
