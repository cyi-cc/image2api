package service

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"

	ort "github.com/yalue/onnxruntime_go"
)

// YuNet 人脸检测模型（opencv_zoo face_detection_yunet_2023mar，输入尺寸已改为
// 动态），编译进二进制，运行时只额外依赖 onnxruntime 动态库。
//
//go:embed assets/yunet.onnx
var yunetModel []byte

const (
	// 检出分数下限与 NMS 的 IoU 阈值
	faceScoreThreshold = 0.35
	faceNMSIoU         = 0.3
	// 推理输入的长边上限：更大的图先等比缩小，检出框再映射回原图，
	// 避免超大参考图把内存和耗时拉爆。
	faceMaxInferSide = 2560
)

// YuNet 的三个输出分支步长
var faceStrides = []int{8, 16, 32}

// ErrNoFaceDetected 表示图中没有检出人脸，调用方按原图处理。
var ErrNoFaceDetected = errors.New("no face detected")

var (
	faceOnce    sync.Once
	faceSession *ort.DynamicAdvancedSession
	faceInitErr error
)

// onnxruntimeLibPath 返回 onnxruntime 动态库路径：ONNXRUNTIME_LIB_PATH 优先，
// 否则用各平台的默认位置。
func onnxruntimeLibPath() string {
	if p := os.Getenv("ONNXRUNTIME_LIB_PATH"); p != "" {
		return p
	}
	if runtime.GOOS == "windows" {
		return "onnxruntime.dll"
	}
	return "/usr/local/lib/libonnxruntime.so"
}

// faceOutputNames 是 YuNet 需要读取的输出名，顺序与 readOutputs 的下标约定一致：
// 先 cls_*、再 obj_*、最后 bbox_*（关键点分支用不到）。
func faceOutputNames() []string {
	names := make([]string, 0, len(faceStrides)*3)
	for _, prefix := range []string{"cls", "obj", "bbox"} {
		for _, s := range faceStrides {
			names = append(names, fmt.Sprintf("%s_%d", prefix, s))
		}
	}
	return names
}

func faceDetector() (*ort.DynamicAdvancedSession, error) {
	faceOnce.Do(func() {
		ort.SetSharedLibraryPath(onnxruntimeLibPath())
		if err := ort.InitializeEnvironment(); err != nil {
			faceInitErr = fmt.Errorf("onnxruntime init: %w", err)
			return
		}
		faceSession, faceInitErr = ort.NewDynamicAdvancedSessionWithONNXData(
			yunetModel, []string{"input"}, faceOutputNames(), nil)
	})
	if faceInitErr != nil {
		return nil, faceInitErr
	}
	return faceSession, nil
}

type faceDetection struct {
	rect  image.Rectangle
	score float32
}

// detectFaces 返回图中的人脸矩形框（坐标基于 src 的原始尺寸）。
func detectFaces(src image.Image) ([]image.Rectangle, error) {
	sess, err := faceDetector()
	if err != nil {
		return nil, err
	}
	bounds := src.Bounds()
	long := bounds.Dx()
	if bounds.Dy() > long {
		long = bounds.Dy()
	}
	scale := 1.0
	if long > faceMaxInferSide {
		scale = float64(faceMaxInferSide) / float64(long)
	}
	inW := int(float64(bounds.Dx()) * scale)
	inH := int(float64(bounds.Dy()) * scale)
	if inW < 1 || inH < 1 {
		return nil, nil
	}
	// 输入补齐到 32 的整数倍，三个步长分支才有整数网格
	padW := (inW + 31) / 32 * 32
	padH := (inH + 31) / 32 * 32

	// YuNet 吃 BGR、NCHW、未归一化的 0~255 像素
	pixels := make([]float32, 3*padW*padH)
	plane := padW * padH
	for y := 0; y < inH; y++ {
		srcY := bounds.Min.Y + int(float64(y)/scale)
		for x := 0; x < inW; x++ {
			r, g, b, _ := src.At(bounds.Min.X+int(float64(x)/scale), srcY).RGBA()
			i := y*padW + x
			pixels[i] = float32(b >> 8)
			pixels[plane+i] = float32(g >> 8)
			pixels[2*plane+i] = float32(r >> 8)
		}
	}
	input, err := ort.NewTensor(ort.NewShape(1, 3, int64(padH), int64(padW)), pixels)
	if err != nil {
		return nil, err
	}
	defer input.Destroy()

	outputs := make([]ort.Value, len(faceStrides)*3)
	if err := sess.Run([]ort.Value{input}, outputs); err != nil {
		return nil, err
	}
	defer func() {
		for _, out := range outputs {
			if out != nil {
				out.Destroy()
			}
		}
	}()

	branch := func(i int) ([]float32, error) {
		t, ok := outputs[i].(*ort.Tensor[float32])
		if !ok {
			return nil, fmt.Errorf("yunet output %d is not a float32 tensor", i)
		}
		return t.GetData(), nil
	}
	inferBounds := image.Rect(0, 0, inW, inH)
	var dets []faceDetection
	for si, stride := range faceStrides {
		cls, err := branch(si)
		if err != nil {
			return nil, err
		}
		obj, err := branch(len(faceStrides) + si)
		if err != nil {
			return nil, err
		}
		box, err := branch(2*len(faceStrides) + si)
		if err != nil {
			return nil, err
		}
		cols, rows := padW/stride, padH/stride
		for row := 0; row < rows; row++ {
			for col := 0; col < cols; col++ {
				idx := row*cols + col
				score := float32(math.Sqrt(float64(clampUnit(cls[idx]) * clampUnit(obj[idx]))))
				if score < faceScoreThreshold {
					continue
				}
				cx := (float32(col) + box[idx*4]) * float32(stride)
				cy := (float32(row) + box[idx*4+1]) * float32(stride)
				w := float32(math.Exp(float64(box[idx*4+2]))) * float32(stride)
				h := float32(math.Exp(float64(box[idx*4+3]))) * float32(stride)
				rect := image.Rect(int(cx-w/2), int(cy-h/2), int(cx+w/2), int(cy+h/2)).Intersect(inferBounds)
				if rect.Dx() > 0 && rect.Dy() > 0 {
					dets = append(dets, faceDetection{rect: rect, score: score})
				}
			}
		}
	}

	boxes := make([]image.Rectangle, 0, len(dets))
	for _, d := range suppressOverlaps(dets, faceNMSIoU) {
		rect := d.rect
		if scale != 1 {
			rect = image.Rect(
				int(float64(rect.Min.X)/scale), int(float64(rect.Min.Y)/scale),
				int(float64(rect.Max.X)/scale), int(float64(rect.Max.Y)/scale),
			).Intersect(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		}
		if rect.Dx() > 0 && rect.Dy() > 0 {
			boxes = append(boxes, rect.Add(bounds.Min))
		}
	}
	return boxes, nil
}

func clampUnit(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// suppressOverlaps 按分数从高到低做 NMS，丢掉与已保留框 IoU 超过阈值的框。
func suppressOverlaps(dets []faceDetection, iouThreshold float64) []faceDetection {
	sort.SliceStable(dets, func(i, j int) bool { return dets[i].score > dets[j].score })
	kept := make([]faceDetection, 0, len(dets))
	for _, d := range dets {
		overlaps := false
		for _, k := range kept {
			if rectIoU(d.rect, k.rect) > iouThreshold {
				overlaps = true
				break
			}
		}
		if !overlaps {
			kept = append(kept, d)
		}
	}
	return kept
}

func rectIoU(a, b image.Rectangle) float64 {
	inter := a.Intersect(b)
	if inter.Empty() {
		return 0
	}
	interArea := float64(inter.Dx() * inter.Dy())
	return interArea / (float64(a.Dx()*a.Dy()+b.Dx()*b.Dy()) - interArea)
}

// applyFaceNotice 给图中每张人脸盖一层红色网格点，返回 PNG。
// 没检出人脸（或不是可解码的图片）时返回 ErrNoFaceDetected，调用方应继续用原图；
// 其它错误说明检测器不可用，调用方不应把未打码的图上传。
func applyFaceNotice(b []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, ErrNoFaceDetected
	}
	boxes, err := detectFaces(src)
	if err != nil {
		return nil, err
	}
	if len(boxes) == 0 {
		return nil, ErrNoFaceDetected
	}

	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	orig := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(orig, orig.Bounds(), src, src.Bounds().Min, draw.Src)

	offset := image.Pt(-src.Bounds().Min.X, -src.Bounds().Min.Y)

	// 脸多（>4 张）时不做顶部条，直接给每张脸盖一层白点阵。
	if len(boxes) > 4 {
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Bounds(), orig, image.Point{}, draw.Src)
		for _, box := range boxes {
			drawWhiteDots(dst, box.Add(offset).Intersect(dst.Bounds()))
		}
		var out bytes.Buffer
		if err := png.Encode(&out, dst); err != nil {
			return nil, err
		}
		return out.Bytes(), nil
	}

	// 紧贴脸的检出框：切片和盖黑都用它，两者一样大。
	heads := make([]image.Rectangle, 0, len(boxes))
	for _, box := range boxes {
		heads = append(heads, box.Add(offset).Intersect(orig.Bounds()))
	}

	// 取面积最大的一张脸切成 4 块——切割线正好穿过脸中心，每块只有 1/4 张脸，
	// 打乱后分开摆到图片上方新加的黑色条带里。
	main := largestRect(heads)
	quads := faceQuadrants(orig, main) // 已按打乱顺序排列的 4 块
	gap := main.Dx() / 8
	if gap < 8 {
		gap = 8
	}
	stripH := main.Dy()/2 + 2*gap

	black := image.NewUniform(color.RGBA{A: 255})
	dst := image.NewRGBA(image.Rect(0, 0, w, h+stripH))
	draw.Draw(dst, dst.Bounds(), black, image.Point{}, draw.Src) // 条带底色黑
	draw.Draw(dst, image.Rect(0, stripH, w, h+stripH), orig, image.Point{}, draw.Src)

	// 4 块横向排开，块间留黑缝，明显是碎片而非整脸。
	x := gap
	for _, q := range quads {
		qw, qh := q.Bounds().Dx(), q.Bounds().Dy()
		if x+qw > w {
			break
		}
		draw.Draw(dst, image.Rect(x, gap, x+qw, gap+qh), q, image.Point{}, draw.Src)
		x += qw + gap
	}

	// 原图里每张脸整块盖黑（坐标下移 stripH）。
	for _, hd := range heads {
		draw.Draw(dst, hd.Add(image.Pt(0, stripH)), black, image.Point{}, draw.Src)
	}

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// expandFaceBox 把 YuNet 的紧致人脸框向外扩到大致一个头部的范围（额头到下巴），
// 只挡眼睛不足以让 Adobe 认不出大图正脸。结果裁剪到图像范围内。
func expandFaceBox(b image.Rectangle, bounds image.Rectangle) image.Rectangle {
	w := b.Dx()
	h := b.Dy()
	padX := w * 60 / 100
	minX := b.Min.X - padX
	maxX := b.Max.X + padX
	width := maxX - minX
	// 高度按脸宽推算（额头到下巴 ≈ 1.2×脸宽）。YuNet 框常只圈住眼部，
	// 只按框高向下扩不足以盖住嘴和下巴。
	height := width * 115 / 100
	if hh := h * 180 / 100; hh > height {
		height = hh
	}
	top := b.Min.Y - h*85/100
	out := image.Rect(minX, top, maxX, top+height)
	return out.Intersect(bounds)
}

// faceQuadrants 把 box 区域切成 2×2 四块，按固定置换打乱顺序后返回这 4 块子图。
func faceQuadrants(src *image.RGBA, box image.Rectangle) []*image.RGBA {
	hw, hh := box.Dx()/2, box.Dy()/2
	rects := [4]image.Rectangle{
		image.Rect(box.Min.X, box.Min.Y, box.Min.X+hw, box.Min.Y+hh),
		image.Rect(box.Min.X+hw, box.Min.Y, box.Max.X, box.Min.Y+hh),
		image.Rect(box.Min.X, box.Min.Y+hh, box.Min.X+hw, box.Max.Y),
		image.Rect(box.Min.X+hw, box.Min.Y+hh, box.Max.X, box.Max.Y),
	}
	perm := [4]int{3, 1, 2, 0} // 打乱顺序
	rot := [4]int{1, 2, 3, 2}  // 每块各自旋转 90°/180°/270°，打断人脸连续性
	out := make([]*image.RGBA, 0, 4)
	for i, s := range perm {
		r := rects[s]
		q := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
		draw.Draw(q, q.Bounds(), src, r.Min, draw.Src)
		out = append(out, rotateRGBA(q, rot[i]))
	}
	return out
}

// rotateRGBA 顺时针旋转 90° 的 k 倍，返回旋转后的新图。
func rotateRGBA(src *image.RGBA, k int) *image.RGBA {
	k = ((k % 4) + 4) % 4
	if k == 0 {
		return src
	}
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	var dst *image.RGBA
	if k == 2 {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dst.SetRGBA(w-1-x, h-1-y, src.RGBAAt(x, y))
			}
		}
		return dst
	}
	dst = image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if k == 1 {
				dst.SetRGBA(h-1-y, x, src.RGBAAt(x, y))
			} else {
				dst.SetRGBA(y, w-1-x, src.RGBAAt(x, y))
			}
		}
	}
	return dst
}

// drawWhiteDots 在脸框内铺一层小白点阵，盖住五官（只盖脸、不外扩）。
func drawWhiteDots(dst *image.RGBA, box image.Rectangle) {
	step := box.Dx() / 16
	if step < 4 {
		step = 4
	}
	r := step / 3
	if r < 2 {
		r = 2
	}
	white := color.RGBA{R: 255, G: 255, B: 255, A: 255}
	for cy := box.Min.Y + step/2; cy < box.Max.Y; cy += step {
		for cx := box.Min.X + step/2; cx < box.Max.X; cx += step {
			fillDot(dst, cx, cy, r, box, white)
		}
	}
}

// largestRect 返回面积最大的矩形。
func largestRect(rs []image.Rectangle) image.Rectangle {
	best := rs[0]
	for _, r := range rs[1:] {
		if r.Dx()*r.Dy() > best.Dx()*best.Dy() {
			best = r
		}
	}
	return best
}

// minInt 返回两数中较小者。
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// faceDotAlpha 网格点的不透明度（255 = 实心）。
const faceDotAlpha uint8 = 26

// blendPixel 把前景色按其 alpha 叠到底色上。
func blendPixel(bg, fg color.RGBA) color.RGBA {
	a := int(fg.A)
	mix := func(f, b uint8) uint8 {
		return uint8((int(f)*a + int(b)*(255-a)) / 255)
	}
	return color.RGBA{R: mix(fg.R, bg.R), G: mix(fg.G, bg.G), B: mix(fg.B, bg.B), A: 255}
}

// fillDot 以 (cx,cy) 为心画一个半径 r 的圆，按 c.A 与底图混合，裁剪在 clip 内。
func fillDot(dst *image.RGBA, cx, cy, r int, clip image.Rectangle, c color.RGBA) {
	r2 := r * r
	for y := cy - r; y <= cy+r; y++ {
		if y < clip.Min.Y || y >= clip.Max.Y {
			continue
		}
		for x := cx - r; x <= cx+r; x++ {
			if x < clip.Min.X || x >= clip.Max.X {
				continue
			}
			dx := x - cx
			dy := y - cy
			if dx*dx+dy*dy <= r2 {
				dst.SetRGBA(x, y, blendPixel(dst.RGBAAt(x, y), c))
			}
		}
	}
}
