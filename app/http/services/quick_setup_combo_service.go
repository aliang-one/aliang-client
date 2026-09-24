package services

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/app/http/storage"
)

// QuickSetupComboService 承载配置组合（套餐）的三入口创建与默认种子（spec §6.1）。
// store 经 quickSetupComboStoreFn 钩子获取（默认真实库），测试注入临时路径 store。
type QuickSetupComboService struct {
	store *storage.QuickSetupComboStore
}

// quickSetupComboStoreFn 是组合 store 的构造钩子：测试注入临时 SQLite 路径的 store，
// 生产默认走 software config 统一库。
var quickSetupComboStoreFn = storage.NewQuickSetupComboStore

func NewQuickSetupComboService() *QuickSetupComboService {
	return &QuickSetupComboService{store: quickSetupComboStoreFn()}
}

// quickSetupComboMaxNameRunes 是组合名长度上限（与存储层 varchar(128) 对齐）。
const quickSetupComboMaxNameRunes = 128

// normalizeQuickSetupComboName 规整组合名：TrimSpace；超长直接截断到 128 rune
// （不报错，spec §6.1 超长截断合法）。空串原样返回，由调用方报错。
func normalizeQuickSetupComboName(name string) string {
	trimmed := strings.TrimSpace(name)
	runes := []rune(trimmed)
	if len(runes) <= quickSetupComboMaxNameRunes {
		return trimmed
	}
	return string(runes[:quickSetupComboMaxNameRunes])
}

// Create 按 source 创建组合（spec §6.1）并返回 API 视图：
//   - blank：files 取空白模板；variables 预填 {base_url: 公网预设, api_key: "", model: ""}，
//     vars 非 nil 时逐键覆盖预填值（handler 的 configure 表单用；本服务测试恒传 nil）；
//   - copy：深拷贝 copyFromID 源组合的 variables/files（忽略 vars/files 参数）；
//     源不存在 → 错误包装 storage.ErrComboNotFound；
//   - disk：quickSetupTargetUserFn 家目录下逐个读 software 声明的配置文件，磁盘内容
//     逐字节入库（不做占位符反推），variables 为空 map；任一文件 missing/unreadable
//     → 报错点名文件（disk 入口只收 ok 态，spec §6.1 文件不存在 → 400）。
//
// files 参数是 handler 任务的预留入口（自定义文件内容直传），本三入口均不消费。
// Create 不做会话校验——鉴权是 handler 层的职责。
func (s *QuickSetupComboService) Create(software, name, source string, copyFromID int64, vars map[string]string, files []models.QuickSetupComboFile) (models.QuickSetupComboView, error) {
	view := models.QuickSetupComboView{}
	declared, ok := findQuickSetupSoftware(software)
	if !ok {
		return view, fmt.Errorf("quick setup software %q is not supported", software)
	}
	normalizedName := normalizeQuickSetupComboName(name)
	if normalizedName == "" {
		return view, errors.New("combo name is required")
	}

	var (
		variables  map[string]string
		comboFiles []models.QuickSetupComboFile
	)
	switch source {
	case "blank":
		comboFiles = append(comboFiles, quickSetupComboBlankTemplates(declared.Code)...)
		_, publicBaseURL, err := quickSetupBaseURLPresets(declared.Code)
		if err != nil {
			return view, err
		}
		variables = map[string]string{"base_url": publicBaseURL, "api_key": "", "model": ""}
		for key, value := range vars {
			variables[key] = value
		}
	case "copy":
		sourceRow, err := s.store.GetByID(copyFromID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return view, fmt.Errorf("%w: id %d", storage.ErrComboNotFound, copyFromID)
			}
			return view, err
		}
		variables = make(map[string]string, len(sourceRow.Variables))
		for key, value := range sourceRow.Variables {
			variables[key] = value
		}
		comboFiles = make([]models.QuickSetupComboFile, len(sourceRow.Files))
		copy(comboFiles, sourceRow.Files)
	case "disk":
		target, err := quickSetupTargetUserFn()
		if err != nil {
			return view, err
		}
		comboFiles = make([]models.QuickSetupComboFile, 0, len(declared.Files))
		for _, fileDef := range declared.Files {
			content, state := quickSetupReadExistingFile(declared.Code, fileDef.DefaultPath, target.homeDir)
			if state != quickSetupReadOK {
				return view, fmt.Errorf("cannot import %s from disk: file is missing or unreadable", quickSetupLeafFileName(fileDef.DefaultPath))
			}
			comboFiles = append(comboFiles, models.QuickSetupComboFile{Code: fileDef.Code, Content: content})
		}
		variables = map[string]string{}
	default:
		return view, errors.New("source is not valid")
	}

	row := &models.QuickSetupCombo{
		Software:  declared.Code,
		Name:      normalizedName,
		Variables: variables,
		Files:     comboFiles,
	}
	if err := s.store.Create(row); err != nil {
		return view, err
	}
	created, err := storage.ComboToView(row)
	if err != nil {
		return view, err
	}
	return *created, nil
}

// SeedIfEmpty 为 software 幂等种子「默认」组合：空库时以 blank 入口创建并置
// is_default=true（store 层 Update 支持）。Create 撞 ErrComboNameTaken 视为
// 「已被并发种子」吞掉返回 nil，其余错误上抛（否则会把 Catalog 打成 failed）。
func (s *QuickSetupComboService) SeedIfEmpty(software string) error {
	combos, err := s.ListBySoftware(software)
	if err != nil {
		return err
	}
	if len(combos) > 0 {
		return nil
	}
	created, err := s.Create(software, "默认", "blank", 0, nil, nil)
	if err != nil {
		if errors.Is(err, storage.ErrComboNameTaken) {
			return nil
		}
		return err
	}
	row, err := s.store.GetByID(created.ID)
	if err != nil {
		return err
	}
	row.IsDefault = true
	return s.store.Update(row)
}

// ListBySoftware 返回该 software 的全部组合（默认在前，store 层排序），转为 API 视图。
func (s *QuickSetupComboService) ListBySoftware(software string) ([]models.QuickSetupComboView, error) {
	declared, ok := findQuickSetupSoftware(software)
	if !ok {
		return nil, fmt.Errorf("quick setup software %q is not supported", software)
	}
	rows, err := s.store.ListBySoftware(declared.Code)
	if err != nil {
		return nil, err
	}
	views := make([]models.QuickSetupComboView, 0, len(rows))
	for i := range rows {
		view, err := storage.ComboToView(&rows[i])
		if err != nil {
			return nil, err
		}
		views = append(views, *view)
	}
	return views, nil
}

// getComboRow 取组合行并把「不存在」翻译为 storage.ErrComboNotFound
// （store 的 GetByID 原样上抛 gorm.ErrRecordNotFound，哨兵翻译是 service 层职责）。
func (s *QuickSetupComboService) getComboRow(id int64) (*models.QuickSetupCombo, error) {
	row, err := s.store.GetByID(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: id %d", storage.ErrComboNotFound, id)
		}
		return nil, err
	}
	return row, nil
}

// Update 部分更新组合（spec §6.2）：name/vars/files 均为可选（nil 不动）。
// files 提供时其 code 必须都在该 software 声明内；撞名上抛 store 的 ErrComboNameTaken。
func (s *QuickSetupComboService) Update(id int64, name *string, vars map[string]string, files []models.QuickSetupComboFile) (models.QuickSetupComboView, error) {
	row, err := s.getComboRow(id)
	if err != nil {
		return models.QuickSetupComboView{}, err
	}
	if name != nil {
		normalizedName := normalizeQuickSetupComboName(*name)
		if normalizedName == "" {
			return models.QuickSetupComboView{}, errors.New("combo name is required")
		}
		row.Name = normalizedName
	}
	if vars != nil {
		row.Variables = vars
	}
	if files != nil {
		declared, ok := findQuickSetupSoftware(row.Software)
		if !ok {
			return models.QuickSetupComboView{}, fmt.Errorf("quick setup software %q is not supported", row.Software)
		}
		valid := make(map[string]struct{}, len(declared.Files))
		for _, fileDef := range declared.Files {
			valid[fileDef.Code] = struct{}{}
		}
		for _, file := range files {
			if _, ok := valid[file.Code]; !ok {
				return models.QuickSetupComboView{}, fmt.Errorf("file code is not valid: %s", file.Code)
			}
		}
		row.Files = files
	}
	if err := s.store.Update(row); err != nil {
		return models.QuickSetupComboView{}, err
	}
	latest, err := s.getComboRow(id)
	if err != nil {
		return models.QuickSetupComboView{}, err
	}
	view, err := storage.ComboToView(latest)
	if err != nil {
		return models.QuickSetupComboView{}, err
	}
	return *view, nil
}

// Delete 删除组合（不存在 → storage.ErrComboNotFound）。
func (s *QuickSetupComboService) Delete(id int64) error {
	if _, err := s.getComboRow(id); err != nil {
		return err
	}
	return s.store.Delete(id)
}

// SetDefault 查出组合取 software 再交 store 事务置默认
// （每 software 至多一个 default；跨 software 防护由 store 保证）。
func (s *QuickSetupComboService) SetDefault(id int64) error {
	row, err := s.getComboRow(id)
	if err != nil {
		return err
	}
	return s.store.SetDefault(row.Software, id)
}

// SetDefaultAndList 置默认并返回该 software 的全部组合（set-default 端点契约：
// 一次调用带回最新列表，省去前端二次拉取）。
func (s *QuickSetupComboService) SetDefaultAndList(id int64) ([]models.QuickSetupComboView, error) {
	row, err := s.getComboRow(id)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetDefault(row.Software, id); err != nil {
		return nil, err
	}
	return s.ListBySoftware(row.Software)
}
