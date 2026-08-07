package service

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"image"
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

// applyFaceNotice 给图中每张人脸盖一层黑丝网眼，返回 PNG。
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
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)

	offset := image.Pt(-src.Bounds().Min.X, -src.Bounds().Min.Y)
	for _, box := range boxes {
		r := box.Add(offset).Intersect(dst.Bounds())
		// 网眼只盖脸中央，留出边缘的发型与轮廓。
		r = image.Rect(r.Min.X+r.Dx()/8, r.Min.Y+r.Dy()/8, r.Max.X-r.Dx()/8, r.Max.Y-r.Dy()/8)
		drawStocking(dst, r)
	}

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// drawStocking 在给定区域上盖一层黑丝网眼：细密的深色网格线，遮住五官细节但保留轮廓。
func drawStocking(dst *image.RGBA, r image.Rectangle) {
	r = r.Intersect(dst.Bounds())
	step := r.Dx() / 24
	if step < 3 {
		step = 3
	}
	line := step / 2
	if line < 1 {
		line = 1
	}
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if (x-r.Min.X)%step >= line && (y-r.Min.Y)%step >= line {
				continue
			}
			c := dst.RGBAAt(x, y)
			c.R = uint8(uint32(c.R) * 10 / 100)
			c.G = uint8(uint32(c.G) * 10 / 100)
			c.B = uint8(uint32(c.B) * 10 / 100)
			dst.SetRGBA(x, y, c)
		}
	}
}

// faceMaskPromptNote 附加到 Seedance 提示词后：告知模型参考图脸部的网格线只是打码，
// 需要忽略网格本身并完整还原面部细节。
const faceMaskPromptNote = "参考图人物脸部覆盖的细密网格线仅为隐私打码，不是人物本身的特征：生成时请完全忽略这些网格线，不要在画面中出现任何网格、方格、纹理或遮挡；请依据参考图的五官轮廓完整还原人物真实面孔，保留妆容、眉眼、发型、发饰、耳饰、头冠等一切面部与头部装饰细节，人物面部必须清晰完整、前后镜头保持一致。"
