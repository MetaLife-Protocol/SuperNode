package cases

import (
	"fmt"
	"time"

	"github.com/MetaLife-Protocol/SuperNode/cmd/tools/casemanager/models"
	"github.com/MetaLife-Protocol/SuperNode/params"
)

// CrashCase010 : only for local test
func (cm *CaseManager) CrashCase010() (err error) {
	env, err := models.NewTestEnv("./cases/CrashCase010.ENV", cm.UseMatrix, cm.EthEndPoint)
	if err != nil {
		return
	}
	defer func() {
		if env.Debug == false {
			env.KillAllPhotonNodes()
		}
	}()
	models.Logger.Println(env.CaseName + " BEGIN ====>")
	n0, n1, n2 := env.Nodes[0], env.Nodes[1], env.Nodes[2]
	transAmount := int32(10)
	tokenAddress := env.Tokens[0].TokenAddress.String()
	// 启动
	cm.startNodes(env, n0, n2,
		n1.SetConditionQuit(&params.ConditionQuit{
			QuitEvent: "ReceiveUnlockStateChange",
		}))
	// 初始数据记录
	n0.GetChannelWith(n1, tokenAddress).PrintDataBeforeTransfer()
	n1.GetChannelWith(n2, tokenAddress).PrintDataBeforeTransfer()
	// 转账
	go n0.SendTrans(tokenAddress, transAmount, n2.Address, false)
	time.Sleep(time.Second * 3)
	// 崩溃判断
	for i := 0; i < cm.HighMediumWaitSeconds; i++ {
		time.Sleep(time.Second)
		if !n1.IsRunning() {
			break
		}
	}
	if n1.IsRunning() {
		msg := "Node " + n1.Name + " should be exited,but it still running, FAILED !!!"
		models.Logger.Println(msg)
		return fmt.Errorf(msg)
	}
	// 中间数据记录
	models.Logger.Println("------------ Data After Crash ------------")
	n0.GetChannelWith(n1, tokenAddress).PrintDataAfterCrash()
	n2.GetChannelWith(n1, tokenAddress).PrintDataAfterCrash()
	// 重启
	time.Sleep(30 * time.Second)
	n1.ReStartWithoutConditionquit(env)
	for i := 0; i < cm.HighMediumWaitSeconds; i++ {
		time.Sleep(time.Second)
		// 查询重启后数据
		models.Logger.Println("------------ Data After Restart ------------")
		c01new := n0.GetChannelWith(n1, tokenAddress).PrintDataAfterRestart()
		c12new := n1.GetChannelWith(n2, tokenAddress).PrintDataAfterRestart()
		// 校验对等
		models.Logger.Println("------------ Data After Fail ------------")
		if !c01new.CheckEqualByPartnerNode(env) || !c12new.CheckEqualByPartnerNode(env) {
			continue
		}
		if !c01new.CheckLockBoth(0) {
			continue
		}
		if !c12new.CheckLockBoth(0) {
			continue
		}
		models.Logger.Println(env.CaseName + " END ====> SUCCESS")
		return
	}
	return cm.caseFail(env.CaseName)
}
