package service

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/internal/infra/logger"
	"github.com/QuantumNous/new-api/internal/legacy/model"
	relaycommon "github.com/QuantumNous/new-api/internal/legacy/relay/common"
	billingcontract "github.com/QuantumNous/new-api/internal/module/billing/contract"
	"github.com/QuantumNous/new-api/internal/shared/common"
	"github.com/QuantumNous/new-api/internal/shared/constant"
	"github.com/QuantumNous/new-api/internal/shared/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

const (
	taskBillingActionCompletion = "completion"
	taskBillingActionRefund     = "refund"
	taskBillingActionSubmission = "submission"
	taskBillingActionAdjustment = "adjustment"
	taskBillingActionUnknown    = "unknown"
)

var errTaskBillingOutcomeUnknown = errors.New("task billing outcome is unresolved")

type taskBillingPlan struct {
	action      string
	targetQuota int
	delta       int
	operationID string
	pending     bool
	reason      string
	clamps      []*common.QuotaClamp
}

func taskBillingOperationID(taskID int64, action string) string {
	if taskID <= 0 || action == "" {
		return ""
	}
	return fmt.Sprintf("task:%d:%s", taskID, action)
}

func taskBillingRequest(ctx context.Context, tx *gorm.DB, task *model.Task) (billingcontract.BillingRequest, error) {
	if task == nil || task.ID <= 0 {
		return billingcontract.BillingRequest{}, errors.New("task billing requires a persisted task")
	}
	if task.UserId <= 0 {
		return billingcontract.BillingRequest{}, errors.New("task billing user is invalid")
	}
	source := task.PrivateData.BillingSource
	if source != BillingSourceWallet && source != BillingSourceSubscription {
		return billingcontract.BillingRequest{}, errors.New("task billing source is missing")
	}
	requestID := task.PrivateData.BillingRequestID
	if requestID == "" && task.PrivateData.Execution != nil {
		requestID = task.PrivateData.Execution.RequestID
	}
	if requestID == "" {
		return billingcontract.BillingRequest{}, errors.New("task billing request identity is missing")
	}
	input := billingcontract.BillingRequest{
		RequestID:       requestID,
		ModelName:       taskModelName(task),
		Preference:      "",
		UserID:          task.UserId,
		TokenID:         task.PrivateData.TokenId,
		TokenUnlimited:  task.PrivateData.BillingTokenUnlimited,
		Playground:      task.PrivateData.BillingPlayground,
		ForcePreConsume: true,
	}
	if input.Playground {
		return input, nil
	}
	token, err := model.GetHistoricalTokenForBilling(tx, task.UserId, task.PrivateData.TokenId)
	if err != nil {
		return billingcontract.BillingRequest{}, fmt.Errorf("load historical task token: %w", err)
	}
	input.TokenKey = token.Key
	return input, nil
}

func taskBillingPlanForTarget(task *model.Task, action string, targetQuota int) (taskBillingPlan, error) {
	if task == nil || task.ID <= 0 {
		return taskBillingPlan{}, errors.New("task billing requires a persisted task")
	}
	if targetQuota < 0 || targetQuota > common.MaxQuota {
		return taskBillingPlan{}, fmt.Errorf("task billing target is out of range: %d", targetQuota)
	}
	if task.Quota < 0 || task.Quota > common.MaxQuota {
		return taskBillingPlan{}, fmt.Errorf("task billing quota is out of range: %d", task.Quota)
	}
	if action != taskBillingActionCompletion && action != taskBillingActionRefund {
		return taskBillingPlan{}, fmt.Errorf("invalid task billing action: %s", action)
	}
	delta := targetQuota - task.Quota
	plan := taskBillingPlan{action: action, targetQuota: targetQuota, delta: delta, operationID: taskBillingOperationID(task.ID, action), pending: delta != 0}
	if action == taskBillingActionRefund && task.Quota == 0 {
		plan.pending = false
	}
	return plan, nil
}

func setTaskBillingIntent(task *model.Task, plan taskBillingPlan) {
	if task == nil {
		return
	}
	setTaskBillingAudit(task, plan.clamps...)
	if task.BillingPending && task.BillingAction == taskBillingActionUnknown {
		// Unknown upstream outcomes are a reconciliation fence. A timeout or a
		// later local error must not silently turn them into an automatic refund.
		return
	}
	if !plan.pending {
		clearTaskBillingIntent(task)
		return
	}
	task.BillingPending = true
	task.BillingAction = plan.action
	task.BillingOperationID = plan.operationID
	task.BillingTargetQuota = plan.targetQuota
	task.BillingDelta = plan.delta
}

func setTaskBillingAudit(task *model.Task, clamps ...*common.QuotaClamp) {
	if task == nil || len(clamps) == 0 {
		return
	}
	for _, clamp := range clamps {
		if clamp == nil {
			continue
		}
		task.BillingAudit.QuotaSaturation = clamp.AuditMap()
		return
	}
}

func setTaskBillingUnknown(task *model.Task) {
	if task == nil {
		return
	}
	if task.BillingPending && task.BillingAction != "" {
		return
	}
	forceTaskBillingUnknown(task)
}

// forceTaskBillingUnknown installs the reconciliation fence even for a
// zero-quota task. A missing or ambiguous upstream outcome must remain visible
// and must never be represented by a silently cleared marker.
func forceTaskBillingUnknown(task *model.Task) {
	if task == nil {
		return
	}
	task.BillingPending = true
	task.BillingAction = taskBillingActionUnknown
	task.BillingOperationID = ""
	task.BillingTargetQuota = task.Quota
	task.BillingDelta = 0
}

func clearTaskBillingIntent(task *model.Task) {
	if task == nil {
		return
	}
	task.BillingPending = false
	task.BillingAction = ""
	task.BillingOperationID = ""
	task.BillingTargetQuota = 0
	task.BillingDelta = 0
}

func taskSubmissionBillingPending(task *model.Task) bool {
	if task == nil || !task.BillingPending {
		return false
	}
	return task.BillingAction == taskBillingActionSubmission || task.BillingAction == taskBillingActionAdjustment || task.BillingAction == taskBillingActionUnknown
}

// TaskSubmissionBillingPending reports whether the initial request/session
// hand-off still owns a task row. Fetch handlers use it to defer publishing a
// terminal upstream status until the initial settlement has committed.
func TaskSubmissionBillingPending(task *model.Task) bool {
	return taskSubmissionBillingPending(task)
}

// TaskBillingStatus exposes the accounting lifecycle without leaking receipt
// or token identifiers into task responses.
func TaskBillingStatus(task *model.Task) string {
	if task == nil {
		return "reconciliation"
	}
	if !task.BillingPending && task.BillingAction == "" {
		return "settled"
	}
	if task.BillingAction == taskBillingActionUnknown || !task.BillingPending {
		return "reconciliation"
	}
	return "pending"
}

// PrepareTaskSubmissionBilling records the durable hand-off before the task
// row is inserted. Session-backed requests clear it from the session's commit
// callback; free/no-session requests use the adjustment receipt in that same
// callback. Keeping the marker on the task closes the insert-to-settlement
// crash window.
func PrepareTaskSubmissionBilling(info *relaycommon.RelayInfo, task *model.Task) error {
	if info == nil || task == nil {
		return errors.New("task submission billing identity is missing")
	}
	if info.RequestId == "" {
		return errors.New("task submission billing request identity is missing")
	}
	if task.Quota < 0 || task.Quota > common.MaxQuota {
		return fmt.Errorf("task submission quota is out of range: %d", task.Quota)
	}
	if info.Billing == nil && task.Quota != 0 {
		return errors.New("task without a billing session cannot carry a positive quota")
	}
	if task.PrivateData.BillingSource == "" {
		// A free task still records its request statistic. Treat its zero-value
		// funding source as wallet for the zero-delta adjustment path.
		task.PrivateData.BillingSource = BillingSourceWallet
	}
	if task.PrivateData.UpstreamTaskID == "" && task.Status != model.TaskStatusSuccess && task.Status != model.TaskStatusFailure {
		// A submission without a provider task id has no known upstream outcome.
		// Keep the request/session in reconcile state and persist the task for
		// control-plane review; even a zero-quota task needs an explicit marker so
		// it is not later mistaken for a successful free submission.
		forceTaskBillingUnknown(task)
		return nil
	}
	task.BillingPending = true
	task.BillingTargetQuota = task.Quota
	task.BillingDelta = 0
	task.BillingOperationID = "request:" + info.RequestId + ":submission"
	if info.Billing != nil {
		task.BillingAction = taskBillingActionSubmission
	} else {
		task.BillingAction = taskBillingActionAdjustment
	}
	return nil
}

// CompleteTaskSubmissionBillingTx is passed to Session.SettleWithUsageAndCommit
// by the task controller. It clears a session-backed submission marker after
// the session has committed, or applies the zero-delta statistic for a free
// task through the durable adjustment receipt. It must run in the caller's
// transaction and never opens a nested transaction.
func CompleteTaskSubmissionBillingTx(tx *gorm.DB, taskID int64) error {
	if tx == nil {
		return errors.New("task submission billing transaction is nil")
	}
	task, err := model.LockTaskForBilling(tx, taskID)
	if err != nil {
		return err
	}
	if !task.BillingPending {
		return nil
	}
	if task.BillingAction == taskBillingActionUnknown {
		// Unlike a later known completion/refund marker, an unknown outcome must
		// abort the surrounding session transaction so an active reservation is
		// not settled while the task is fenced for reconciliation.
		return errTaskBillingOutcomeUnknown
	}
	if task.BillingAction != taskBillingActionSubmission && task.BillingAction != taskBillingActionAdjustment {
		// A terminal poll/recovery may have advanced the task marker while an
		// older submission settlement is replayed. The newer stage owns the row;
		// never overwrite it or make the older session transaction fail.
		return nil
	}
	if task.BillingOperationID == "" || task.BillingTargetQuota != task.Quota || task.BillingDelta != 0 {
		return errors.New("task submission billing marker is invalid")
	}
	if task.BillingAction == taskBillingActionAdjustment {
		ctx := tx.Statement.Context
		if ctx == nil {
			return errors.New("task submission billing transaction context is missing")
		}
		input, err := taskBillingRequest(ctx, tx, task)
		if err != nil {
			return err
		}
		if _, err := billingEngine().ApplyAdjustmentTx(ctx, tx, input, billingcontract.BillingAdjustment{
			OperationID:        task.BillingOperationID,
			Source:             task.PrivateData.BillingSource,
			SubscriptionID:     task.PrivateData.SubscriptionId,
			RequestDelta:       1,
			UseHistoricalToken: true,
		}); err != nil {
			return err
		}
	}
	if task.Status == model.TaskStatusFailure && task.Quota != 0 && task.BillingAction == taskBillingActionSubmission {
		plan, err := taskBillingPlanForTarget(task, taskBillingActionRefund, 0)
		if err != nil {
			return err
		}
		setTaskBillingIntent(task, plan)
		return task.UpdateBillingStateTx(tx)
	}
	clearTaskBillingIntent(task)
	return task.UpdateBillingStateTx(tx)
}

func recordTaskBillingAdjustmentLog(task *model.Task, preConsumedQuota, actualQuota int, reason string, clamps ...*common.QuotaClamp) {
	if task == nil {
		return
	}
	delta := actualQuota - preConsumedQuota
	if delta == 0 {
		return
	}
	logType, logQuota := model.LogTypeRefund, -delta
	if delta > 0 {
		logType, logQuota = model.LogTypeConsume, delta
	}
	other := taskBillingOther(task)
	other.SetPublic("task_id", task.TaskID)
	other.SetPublic("pre_consumed_quota", preConsumedQuota)
	other.SetPublic("actual_quota", actualQuota)
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
}

func persistTaskBillingUnknown(ctx context.Context, taskID int64) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		task, err := model.LockTaskForBilling(tx, taskID)
		if err != nil {
			return err
		}
		setTaskBillingUnknown(task)
		return task.UpdateBillingStateTx(tx)
	})
}

// persistTaskBillingPlan records a requested terminal intent after an attempt
// failed before the ledger transaction could commit. It is the recovery fence
// for callers that compute a terminal plan directly (for example an explicit
// refund helper); successful ledger writes still clear the marker in the same
// transaction below.
func persistTaskBillingPlan(ctx context.Context, taskID int64, plan taskBillingPlan) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := model.LockTaskForBilling(tx, taskID)
		if err != nil {
			return err
		}
		if current.BillingPending {
			return nil
		}
		if current.Status != model.TaskStatusSuccess && current.Status != model.TaskStatusFailure {
			return errors.New("task billing outcome is not terminal")
		}
		if plan.action != taskBillingActionCompletion && plan.action != taskBillingActionRefund {
			return errors.New("invalid task billing action")
		}
		if plan.targetQuota < 0 || plan.targetQuota > common.MaxQuota || current.Quota < 0 || current.Quota > common.MaxQuota {
			return errors.New("task billing target is out of range")
		}
		if int64(current.Quota)+int64(plan.delta) != int64(plan.targetQuota) || plan.operationID != taskBillingOperationID(current.ID, plan.action) {
			return errors.New("task billing plan conflicts with current quota")
		}
		setTaskBillingIntent(current, plan)
		return current.UpdateBillingStateTx(tx)
	})
}

// applyTaskBillingIntent performs the ledger receipt, usage statistics and
// task quota marker update in one PostgreSQL transaction. A retry locks the
// same task row and reuses the persisted operation ID, so an uncertain commit
// cannot debit the account twice or clear the marker before the ledger wins.
func applyTaskBillingIntent(ctx context.Context, taskID int64, task *model.Task, requested *taskBillingPlan) (billingcontract.QuotaAdjustment, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if taskID <= 0 {
		return billingcontract.QuotaAdjustment{}, errors.New("task billing id is invalid")
	}
	engine := billingEngine()
	var result billingcontract.QuotaAdjustment
	var input billingcontract.BillingRequest
	var committedTask *model.Task
	err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		current, err := model.LockTaskForBilling(tx, taskID)
		if err != nil {
			return err
		}
		if task != nil && task.ID != 0 && task.ID != current.ID {
			return errors.New("task billing id changed")
		}
		if !current.BillingPending && requested != nil {
			if requested.action != taskBillingActionCompletion && requested.action != taskBillingActionRefund {
				return errors.New("invalid task billing action")
			}
			if requested.targetQuota < 0 || requested.targetQuota > common.MaxQuota {
				return errors.New("task billing target is out of range")
			}
			requestedDelta := requested.targetQuota - current.Quota
			if requestedDelta != requested.delta || requested.operationID != taskBillingOperationID(current.ID, requested.action) {
				return errors.New("task billing plan conflicts with current quota")
			}
			setTaskBillingIntent(current, *requested)
		}
		if !current.BillingPending {
			// An already-cleared task is an idempotent no-op. This also makes a
			// stale recovery row harmless after a successful earlier attempt.
			committedTask = current
			return nil
		}
		if current.Status != model.TaskStatusSuccess && current.Status != model.TaskStatusFailure {
			return errors.New("task billing outcome is not terminal")
		}
		if current.BillingAction == taskBillingActionUnknown || current.BillingOperationID == "" {
			return errors.New("task billing outcome is unresolved")
		}
		if current.BillingAction != taskBillingActionCompletion && current.BillingAction != taskBillingActionRefund {
			return errors.New("task billing action is unresolved")
		}
		if current.BillingTargetQuota < 0 || current.BillingTargetQuota > common.MaxQuota || current.Quota < 0 || current.Quota > common.MaxQuota {
			return errors.New("task billing quota marker is invalid")
		}
		if int64(current.Quota)+int64(current.BillingDelta) != int64(current.BillingTargetQuota) {
			return errors.New("task billing marker conflicts with current quota")
		}
		input, err = taskBillingRequest(ctx, tx, current)
		if err != nil {
			return err
		}
		adjustment := billingcontract.BillingAdjustment{
			OperationID:        current.BillingOperationID,
			Source:             current.PrivateData.BillingSource,
			SubscriptionID:     current.PrivateData.SubscriptionId,
			Delta:              current.BillingDelta,
			UsageDelta:         current.BillingDelta,
			ChannelID:          current.ChannelId,
			UseHistoricalToken: true,
		}
		if adjustment.Delta != 0 {
			if err := engine.ValidateRequestBindingTx(ctx, tx, input, adjustment.Source, adjustment.SubscriptionID); err != nil {
				return err
			}
		}
		result, err = engine.ApplyAdjustmentTx(ctx, tx, input, adjustment)
		if err != nil {
			return err
		}
		current.Quota = current.BillingTargetQuota
		clearTaskBillingIntent(current)
		if err := current.UpdateBillingStateTx(tx); err != nil {
			return err
		}
		committedTask = current
		return nil
	})
	if err != nil {
		if requested != nil {
			if persistErr := persistTaskBillingPlan(ctx, taskID, *requested); persistErr != nil {
				return billingcontract.QuotaAdjustment{}, errors.Join(err, fmt.Errorf("persist task billing intent: %w", persistErr))
			}
		}
		return billingcontract.QuotaAdjustment{}, err
	}
	if committedTask != nil && task != nil {
		*task = *committedTask
	}
	if input.UserID > 0 {
		engine.PublishCommitted(input)
	}
	return result, nil
}

func runPendingTaskTerminalBilling(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var tasks []*model.Task
	if err := model.DB.WithContext(ctx).
		Where("billing_pending = ? AND billing_action IN ?", true, []string{taskBillingActionCompletion, taskBillingActionRefund}).
		Order("id asc").Limit(500).Find(&tasks).Error; err != nil {
		return err
	}
	var recoveryErr error
	for _, task := range tasks {
		if task == nil {
			continue
		}
		preConsumed := task.Quota
		actualQuota := task.BillingTargetQuota
		result, err := applyTaskBillingIntent(ctx, task.ID, task, nil)
		if err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover task billing %s: %w", task.TaskID, err))
			continue
		}
		if !result.Replayed {
			recordTaskBillingAdjustmentLog(task, preConsumed, actualQuota, "任务账务恢复")
		}
	}
	return recoveryErr
}

func runPendingTaskSubmissionBilling(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var tasks []*model.Task
	if err := model.DB.WithContext(ctx).
		Where("billing_pending = ? AND billing_action IN ?", true, []string{taskBillingActionSubmission, taskBillingActionAdjustment}).
		Order("id asc").Limit(500).Find(&tasks).Error; err != nil {
		return err
	}
	var recoveryErr error
	engine := billingEngine()
	for _, task := range tasks {
		if task == nil {
			continue
		}
		if task.BillingAction == taskBillingActionSubmission {
			requestID := task.PrivateData.BillingRequestID
			if requestID == "" && task.PrivateData.Execution != nil {
				requestID = task.PrivateData.Execution.RequestID
			}
			if requestID == "" {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover task billing %s: request identity is missing", task.TaskID))
				continue
			}
			session, err := engine.Resume(ctx, requestID)
			if err == nil {
				err = session.SettleWithUsageAndCommit(ctx, task.BillingTargetQuota, task.ChannelId, func(tx *gorm.DB) error {
					return CompleteTaskSubmissionBillingTx(tx, task.ID)
				})
			}
			if err != nil {
				if errors.Is(err, errTaskBillingOutcomeUnknown) {
					continue
				}
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover task submission %s: %w", task.TaskID, err))
				continue
			}
			// CompleteTaskSubmissionBillingTx records a known immediate FAILURE as
			// the independent refund intent in the same session transaction. Do not
			// inspect this pre-recovery snapshot or issue a second refund here: a
			// concurrent terminal worker may already own the newer marker.
			continue
		}

		var input billingcontract.BillingRequest
		err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			current, err := model.LockTaskForBilling(tx, task.ID)
			if err != nil {
				return err
			}
			input, err = taskBillingRequest(ctx, tx, current)
			if err != nil {
				return err
			}
			return CompleteTaskSubmissionBillingTx(tx, task.ID)
		})
		if err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover free task submission %s: %w", task.TaskID, err))
			continue
		}
		engine.PublishCommitted(input)
	}
	return recoveryErr
}

// RunPendingTaskBilling is the control-plane recovery entry point for task
// submission, terminal task adjustments and Midjourney billing. It processes
// only explicit known intents; rows marked unknown remain pending for manual
// reconciliation.
func RunPendingTaskBilling(ctx context.Context) error {
	return errors.Join(
		runPendingTaskSubmissionBilling(ctx),
		runPendingTaskTerminalBilling(ctx),
		runPendingMidjourneyBilling(ctx),
	)
}

// LogTaskConsumption 记录任务消费日志和统计信息（仅记录，不涉及实际扣费）。
// 实际扣费已由 BillingSession（PreConsumeBilling + SettleBilling）完成。
func LogTaskConsumption(c *gin.Context, info *relaycommon.RelayInfo, task *model.Task) {
	tokenName := c.GetString("token_name")
	logContent := fmt.Sprintf("操作 %s", info.Action)
	// 支持任务仅按次计费
	if common.StringsContains(constant.TaskPricePatches, info.OriginModelName) {
		logContent = fmt.Sprintf("%s，按次计费", logContent)
	} else {
		var contents []string
		if otherRatios := info.PriceData.OtherRatios(); len(otherRatios) > 0 {
			for key, ra := range otherRatios {
				if 1.0 != ra {
					contents = append(contents, fmt.Sprintf("%s: %.2f", key, ra))
				}
			}
		}
		if snap := info.TieredBillingSnapshot; snap != nil {
			for key, value := range snap.UsageFacts {
				contents = append(contents, fmt.Sprintf("%s: %v", key, value))
			}
		}
		if len(contents) > 0 {
			logContent = fmt.Sprintf("%s, 计算参数：%s", logContent, strings.Join(contents, ", "))
		}
	}
	other := model.NewLogOther()
	other.SetPublic("is_task", true)
	other.SetPublic("request_path", c.Request.URL.Path)
	other.SetPublic("model_price", info.PriceData.ModelPrice)
	if info.PriceData.ModelRatio > 0 {
		other.SetPublic("model_ratio", info.PriceData.ModelRatio)
	}
	other.SetPublic("group_ratio", info.PriceData.GroupRatioInfo.GroupRatio)
	if info.PriceData.GroupRatioInfo.HasSpecialRatio {
		other.SetPublic("user_group_ratio", info.PriceData.GroupRatioInfo.GroupSpecialRatio)
	}
	if info.IsModelMapped {
		other.SetPublic("is_model_mapped", true)
		other.SetPublic("upstream_model_name", info.UpstreamModelName)
	}
	if snap := info.TieredBillingSnapshot; snap != nil {
		other.SetPublic("billing_mode", "tiered_expr")
		other.SetPublic("expr_b64", base64.StdEncoding.EncodeToString([]byte(snap.ExprString)))
		other.SetPublic("matched_tier", snap.EstimatedTier)
		if len(snap.UsageFacts) > 0 {
			other.SetPublic("usage_facts", snap.UsageFacts)
		}
	}
	appendTaskLogInfo(task, other)
	attachQuotaSaturation(c, info, other)
	model.RecordConsumeLog(c, info.UserId, model.RecordConsumeLogParams{
		ChannelId: info.ChannelId,
		ModelName: info.OriginModelName,
		TokenName: tokenName,
		Quota:     info.PriceData.Quota,
		Content:   logContent,
		TokenId:   info.TokenId,
		Group:     info.UsingGroup,
		Other:     other,
	})
}

// ---------------------------------------------------------------------------
// 异步任务计费辅助函数
// ---------------------------------------------------------------------------

// taskBillingOther 从 task 的 BillingContext 构建日志 Other 字段。
func taskBillingOther(task *model.Task) *model.LogOther {
	other := model.NewLogOther()
	if task != nil && len(task.BillingAudit.QuotaSaturation) > 0 {
		other.SetAdmin("quota_saturation", task.BillingAudit.QuotaSaturation)
	}
	if bc := task.PrivateData.BillingContext; bc != nil {
		other.SetPublic("model_price", bc.ModelPrice)
		if bc.ModelRatio > 0 {
			other.SetPublic("model_ratio", bc.ModelRatio)
		}
		other.SetPublic("group_ratio", bc.GroupRatio)
		if priceData := taskBillingContextPriceData(bc); priceData != nil {
			for k, v := range priceData.OtherRatios() {
				if !other.SetPublic(k, v) {
					common.SysError("task billing other ratio key rejected: " + k)
				}
			}
		}
		if snap := bc.TieredSnapshot; snap != nil {
			other.SetPublic("billing_mode", "tiered_expr")
			other.SetPublic("expr_b64", base64.StdEncoding.EncodeToString([]byte(snap.ExprString)))
			other.SetPublic("matched_tier", snap.EstimatedTier)
			if len(snap.UsageFacts) > 0 {
				other.SetPublic("usage_facts", snap.UsageFacts)
			}
		}
	}
	props := task.Properties
	if props.UpstreamModelName != "" && props.UpstreamModelName != props.OriginModelName {
		other.SetPublic("is_model_mapped", true)
		other.SetPublic("upstream_model_name", props.UpstreamModelName)
	}
	appendTaskLogInfo(task, other)
	return other
}

func appendTaskLogInfo(task *model.Task, other *model.LogOther) {
	if task == nil || other == nil {
		return
	}
	if task.TaskID != "" {
		other.SetPublic("task_id", task.TaskID)
	}
	if task.PrivateData.Execution != nil {
		AppendTaskPluginAuditInfo(other, task.PrivateData.Execution.TaskPlugin)
	}
	if task.PrivateData.UpstreamTaskID == "" && task.PrivateData.NodeName == "" {
		return
	}
	if task.PrivateData.UpstreamTaskID != "" {
		other.SetRoot("upstream_task_id", task.PrivateData.UpstreamTaskID)
	}
	if task.PrivateData.NodeName != "" {
		other.SetRoot("node_name", task.PrivateData.NodeName)
	}
}

func taskBillingContextPriceData(bc *model.TaskBillingContext) *types.PriceData {
	if bc == nil || len(bc.OtherRatios) == 0 {
		return nil
	}
	priceData := &types.PriceData{}
	if !priceData.ReplaceOtherRatios(bc.OtherRatios) {
		return nil
	}
	return priceData
}

// taskModelName 从 BillingContext 或 Properties 中获取模型名称。
func taskModelName(task *model.Task) string {
	if bc := task.PrivateData.BillingContext; bc != nil && bc.OriginModelName != "" {
		return bc.OriginModelName
	}
	return task.Properties.OriginModelName
}

// RefundTaskQuota 统一的任务失败退款逻辑。
// 当异步任务失败时，退还资金与令牌额度，并回减用户和渠道用量。
// 返回资金来源是否已成功退还；失败时保留 quota，供显式重试或人工对账。
func RefundTaskQuota(ctx context.Context, task *model.Task, reason string) bool {
	if task == nil || task.ID <= 0 {
		logger.LogWarn(ctx, "任务退款缺少持久化 task id")
		return false
	}
	quota := task.Quota
	if quota == 0 && !task.BillingPending {
		return true
	}
	plan, err := taskBillingPlanForTarget(task, taskBillingActionRefund, 0)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("任务退款计划无效 task %s: %s", task.TaskID, err.Error()))
		return false
	}
	if task.BillingPending && task.BillingAction != taskBillingActionRefund {
		logger.LogWarn(ctx, fmt.Sprintf("任务 %s 已有未完成的 %s 账务阶段，拒绝改为退款", task.TaskID, task.BillingAction))
		return false
	}
	if !plan.pending {
		return true
	}
	result, err := applyTaskBillingIntent(ctx, task.ID, task, &plan)
	if err != nil {
		logger.LogWarn(ctx, fmt.Sprintf("退还任务额度失败 task %s: %s", task.TaskID, err.Error()))
		return false
	}
	if result.Replayed {
		return true
	}

	// The ledger operation and marker clear are committed together above. The
	// log is observability only and therefore cannot cause a second refund.
	other := taskBillingOther(task)
	other.SetPublic("task_id", task.TaskID)
	other.SetPublic("reason", reason)
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   model.LogTypeRefund,
		Content:   "",
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     quota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
	})
	return true
}

// RecalculateTaskQuota 通用的异步差额结算。
// actualQuota 是任务完成后的实际应扣额度，与预扣额度 (task.Quota) 做差额结算。
// reason 用于日志记录（例如 "token重算" 或 "adaptor调整"）。
// clamps 可选：若计算 actualQuota 时发生额度饱和，将其记入日志 admin_info（仅管理员可见）。
func RecalculateTaskQuota(ctx context.Context, task *model.Task, actualQuota int, reason string, clamps ...*common.QuotaClamp) {
	if actualQuota < 0 {
		return
	}
	if task == nil || task.ID <= 0 {
		logger.LogError(ctx, "任务差额结算缺少持久化 task id")
		return
	}
	preConsumedQuota := task.Quota
	quotaDelta := actualQuota - preConsumedQuota

	if quotaDelta == 0 {
		logger.LogInfo(ctx, fmt.Sprintf("任务 %s 预扣费准确（%s，%s）",
			task.TaskID, logger.LogQuota(actualQuota), reason))
		return
	}

	logger.LogInfo(ctx, fmt.Sprintf("任务 %s 差额结算：delta=%s（实际：%s，预扣：%s，%s）",
		task.TaskID,
		logger.LogQuota(quotaDelta),
		logger.LogQuota(actualQuota),
		logger.LogQuota(preConsumedQuota),
		reason,
	))

	plan, err := taskBillingPlanForTarget(task, taskBillingActionCompletion, actualQuota)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("差额结算计划无效 task %s: %s", task.TaskID, err.Error()))
		return
	}
	plan.clamps = clamps
	if task.BillingPending {
		if task.BillingAction != taskBillingActionCompletion || task.BillingTargetQuota != actualQuota {
			logger.LogError(ctx, fmt.Sprintf("差额结算与待处理账务冲突 task %s", task.TaskID))
			return
		}
	}
	result, err := applyTaskBillingIntent(ctx, task.ID, task, &plan)
	if err != nil {
		logger.LogError(ctx, fmt.Sprintf("差额结算账务调整失败 task %s: %s", task.TaskID, err.Error()))
		return
	}
	if result.Replayed {
		return
	}

	var logType int
	var logQuota int
	if quotaDelta > 0 {
		logType = model.LogTypeConsume
		logQuota = quotaDelta
	} else {
		logType = model.LogTypeRefund
		logQuota = -quotaDelta
	}
	other := taskBillingOther(task)
	other.SetPublic("task_id", task.TaskID)
	other.SetPublic("pre_consumed_quota", preConsumedQuota)
	other.SetPublic("actual_quota", actualQuota)
	for _, clamp := range clamps {
		attachQuotaSaturationToOther(other, clamp)
	}
	model.RecordTaskBillingLog(model.RecordTaskBillingLogParams{
		UserId:    task.UserId,
		LogType:   logType,
		Content:   reason,
		ChannelId: task.ChannelId,
		ModelName: taskModelName(task),
		Quota:     logQuota,
		TokenId:   task.PrivateData.TokenId,
		Group:     task.Group,
		Other:     other,
		NodeName:  task.PrivateData.NodeName,
	})
}

// RecalculateTaskQuotaByTokens 根据实际 token 消耗重新计费（异步差额结算）。
// 当任务成功且返回了 totalTokens 时，根据模型倍率和分组倍率重新计算实际扣费额度，
// 与预扣费的差额进行补扣或退还。支持钱包和订阅计费来源。
func calculateTaskQuotaByTokens(task *model.Task, totalTokens int) (int, *common.QuotaClamp, bool, string) {
	if task == nil || totalTokens <= 0 {
		return 0, nil, false, ""
	}
	bc := task.PrivateData.BillingContext
	if bc == nil || bc.ModelRatio <= 0 || bc.GroupRatio < 0 {
		return 0, nil, false, ""
	}
	otherMultiplier := 1.0
	if len(bc.OtherRatios) > 0 {
		priceData := taskBillingContextPriceData(bc)
		if priceData == nil {
			return 0, nil, false, ""
		}
		otherMultiplier = priceData.OtherRatioMultiplier()
	}
	// TaskInfo.TotalTokens is already expressed in the task provider's billing
	// unit. The legacy ratio formula applies the captured ModelRatio directly to
	// that unit and must not consult live pricing configuration during a later
	// poll.
	value := float64(totalTokens) * bc.ModelRatio * bc.GroupRatio * otherMultiplier
	actualQuota, clamp := common.QuotaFromFloatChecked(value)
	reason := fmt.Sprintf("token重算：tokens=%d, modelRatio=%.2f, groupRatio=%.2f, otherMultiplier=%.4f", totalTokens, bc.ModelRatio, bc.GroupRatio, otherMultiplier)
	return actualQuota, clamp, true, reason
}

// RecalculateTaskQuotaByTokens 根据任务提交时保存的计费快照重新计费。
// 缺少完整快照时保留原预扣额度，并由调用方决定是否进入待核对状态。
func RecalculateTaskQuotaByTokens(ctx context.Context, task *model.Task, totalTokens int) bool {
	actualQuota, clamp, ok, reason := calculateTaskQuotaByTokens(task, totalTokens)
	if !ok {
		return false
	}
	RecalculateTaskQuota(ctx, task, actualQuota, reason, clamp)
	return true
}
