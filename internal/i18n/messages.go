package i18n

// zhCN is the default (and fallback) message table.
var zhCN = map[string]string{
	// usage block (printed to stderr)
	"usage": `stai - SourceTree git 工作流的 AI 伴侣

用法:
  stai <command> [flags]

命令:
  gen          从暂存改动生成 commit message 并复制到剪贴板 (M1)
  hook         git 钩子入口:
               stai hook prepare-commit-msg <msg-file> [source]
               stai hook pre-commit
               stai hook pre-push
  pr           从分支 diff 一次生成 PR 标题+描述,整块复制到剪贴板 (M3-A)
  pr-title     从分支 diff 生成一行 PR 标题并复制到剪贴板 (M3-C)
  review       AI 审查暂存改动 (M2)。默认仅建议;-strict 在遇到
               高危问题时以非零退出。可选位置参数:仓库路径。
  review-branch AI 审查当前分支相对 base ref 的改动 (M3-B)。
               默认仅建议;-strict 以非零退出。
  stash-msg    从工作树改动生成 stash 信息并复制到剪贴板 (M3-C)
  explain      解释选中文件 (M3-C)。接受 -file=$FILE(SourceTree
               自定义动作形式)或位置参数路径。
  split        建议如何把暂存 diff 拆分为若干逻辑 commit (M3-C)
  changelog    从最近标签或指定 ref 以来的 git log 生成发布说明 (M3-C)
  mergetool    用逐 hunk AI 建议解决冲突 (M3)
  install      写入 git 钩子并注册 SourceTree 自定义动作
  uninstall    移除 install 写入的一切(仅 stai 拥有的钩子和动作)

配置: ~/.config/stai/config.toml + 仓库级 .stai.toml
环境变量覆盖: STAI_BASE_URL, STAI_API_KEY, STAI_MODEL, STAI_LANG
禁用钩子而不卸载: STAI_DISABLE=1
`,

	// generic
	"copied":           "copied to clipboard",
	"clipboard_failed": "clipboard copy failed: %v",
	"unknown_command":  "unknown command: %s",

	// commands
	"usage_hook":        "usage: stai hook <prepare-commit-msg <msg-file> [source] | pre-commit | pre-push>",
	"unknown_hook":      "unknown hook: %s",
	"usage_pr_title":    "usage: stai pr-title [repo]",
	"usage_split":       "usage: stai split [repo]",
	"usage_changelog":   "usage: stai changelog [since]",
	"ahead_none":        "当前分支没有领先 %s 的提交",
	"worktree_empty":    "工作树没有改动",
	"staged_empty":      "暂存区没有改动",
	"log_empty":         "%s..HEAD 没有提交",
	"no_tag":            "未指定起始标签且无法获取最近标签: %v",
	"select_file_first": "请先在文件列表中选中一个文件",
	"file_empty":        "文件为空: %s",

	// notifications
	"notify_subtitle":  "提交信息已复制到剪贴板，Cmd+V 粘贴到提交框",
	"notify_pr_title":  "PR 标题已复制到剪贴板",
	"notify_stash":     "stash 信息已复制到剪贴板",
	"notify_explain":   "文件说明已复制到剪贴板",
	"notify_split":     "commit 拆分建议已复制到剪贴板",
	"notify_changelog": "changelog 已复制到剪贴板",

	// review output
	"review_ok":             "OK 未发现问题",
	"review_fix_label":      "> 建议修改: ",
	"review_summary":        "high %d,medium %d,low %d,未归类 %d",
	"review_subtitle_done":  "审查完成",
	"review_subtitle_file":  "审查完成,报告已写入文件",
	"review_report_notice":  "问题较多,完整报告已写入 %s\n\n%s",
	"review_report_title":   "# stai 审查报告\n\n",
	"review_blocked_commit": "stai: pre-commit 审查发现高危问题,提交已阻断(绕过:STAI_DISABLE=1 或 git commit --no-verify):",
	"review_blocked_push":   "stai: pre-push 审查发现高危问题,push 已阻断(绕过:STAI_DISABLE=1 或 git push --no-verify):",
	"review_failed_commit":  "review failed, committing as-is: %v",
	"review_failed_push":    "branch review failed, pushing as-is: %v",
	"hook_msg_gen_failed":   "commit message generation failed, committing as-is: %v",

	// install / uninstall
	"install_hook_installed":  "hook installed: %s",
	"install_hook_removed":    "hook removed: %s",
	"install_hook_left_alone": "hook left alone (not written by stai): %s",
	"install_removing_failed": "removing hook: %v",
	"install_add_manually":    "Add them manually: SourceTree → Settings → Custom Actions → Add:",
	"install_actions_failed":  "SourceTree custom actions not registered: %v\n",
	"install_caption":         "  Menu caption:  %s",
	"install_script":          "  Script to run:  %s",
	"install_params":          "  Parameters:    %s",
	"install_registered":      "SourceTree custom actions registered: %s (restart SourceTree)",
	"install_remove_manually": "Remove them manually: SourceTree → Settings → Custom Actions:",
	"install_remove_failed":   "SourceTree custom actions not removed: %v\n",
	"install_removed":         "SourceTree custom actions removed: %d (restart SourceTree)",

	// misc
	"dialog_edit_body":   "可编辑,确定后复制到剪贴板,再到 SourceTree 提交框粘贴",
	"dialog_edit_title":  "stai — 编辑提交信息",
	"dialog_ok":          "确定",
	"dialog_cancel":      "取消",
	"mergetool_not_impl": "stai %s: not implemented yet\n  plan: %s\n",
	"lang_fallback":      "stai: unsupported lang %q, falling back to zh-CN (supported: zh-CN, en)",

	// ai package user-facing errors
	"err_commit_invalid": "模型输出仍不合规",
	"err_pr_invalid":     "PR 标题/描述仍不合规",
	"err_empty_stash":    "模型返回空 stash message",
	"err_empty_pr_title": "模型返回空 PR 标题",

	// install wizard
	"wizard_welcome":          "欢迎使用 stai! 配置不存在,开始引导设置。",
	"wizard_choose_lang":      "选择语言 / Choose language:",
	"wizard_lang_zh":          "中文",
	"wizard_lang_en":          "English",
	"wizard_base_url":         "Provider base URL",
	"wizard_api_key":          "Provider API key",
	"wizard_model_loading":    "正在获取模型列表...",
	"wizard_model_list_empty": "未获取到模型列表,请手动输入模型名。",
	"wizard_model_prompt":     "选择模型",
	"wizard_model_manual":     "无法获取模型列表,请手动输入模型名",
	"wizard_actions_prompt":   "选择要注册的 SourceTree 自定义动作(输入编号,逗号分隔;直接回车=全部;0=不注册)",
	"wizard_actions_all":      "将注册全部 SourceTree 自定义动作",
	"wizard_actions_none":     "跳过 SourceTree 自定义动作",
	"wizard_actions_done":     "已选择 %d 个 SourceTree 自定义动作",
	"wizard_config_written":   "配置文件已写入: %s",
	"wizard_invalid_choice":   "无效选择,请重新输入",

	// uninstall
	"uninstall_config_prompt":  "是否删除全局配置文件 %s?",
	"uninstall_config_removed": "配置文件已删除: %s",
	"uninstall_config_failed":  "删除配置文件失败: %v",
	"uninstall_log_failed":     "删除日志文件失败: %v",
	"uninstall_done":           "卸载完成",
}

// en mirrors zhCN in English.
var en = map[string]string{
	"usage": `stai - AI companion for SourceTree git workflows

Usage:
  stai <command> [flags]

Commands:
  gen          Generate a commit message from staged changes and copy it
               to the clipboard (M1)
  hook         Entrypoint for git hooks:
               stai hook prepare-commit-msg <msg-file> [source]
               stai hook pre-commit
               stai hook pre-push
  pr           Generate the PR title and description from the branch diff in
               one call and copy the combined block to the clipboard (M3-A)
  pr-title     Generate a one-line PR title from the branch diff and copy
               it to the clipboard (M3-C)
  review       AI review of the staged changes (M2). Advisory by default;
               -strict exits non-zero on high-severity findings.
               Optional positional argument: the repository path.
  review-branch AI review of the current branch against its base ref (M3-B).
               Advisory by default; -strict exits non-zero.
  stash-msg    Generate a stash message from working-tree changes and copy
               it to the clipboard (M3-C)
  explain      Explain the selected file (M3-C). Accepts -file=$FILE (the
               SourceTree custom-action form) or a positional path.
  split        Suggest how to split the staged diff into logical commits
               (M3-C)
  changelog    Generate release notes from git log since the last tag or a
               given ref (M3-C)
  mergetool    Resolve conflicts with per-hunk AI suggestions (M3)
  install      Write the git hooks and register the SourceTree custom actions
  uninstall    Remove everything install wrote (stai-owned hooks and
               custom actions only)

Config: ~/.config/stai/config.toml + per-repo .stai.toml
Env overrides: STAI_BASE_URL, STAI_API_KEY, STAI_MODEL, STAI_LANG
Disable hooks without uninstalling: STAI_DISABLE=1
`,

	"copied":           "copied to clipboard",
	"clipboard_failed": "clipboard copy failed: %v",
	"unknown_command":  "unknown command: %s",

	"usage_hook":        "usage: stai hook <prepare-commit-msg <msg-file> [source] | pre-commit | pre-push>",
	"unknown_hook":      "unknown hook: %s",
	"usage_pr_title":    "usage: stai pr-title [repo]",
	"usage_split":       "usage: stai split [repo]",
	"usage_changelog":   "usage: stai changelog [since]",
	"ahead_none":        "current branch has no commits ahead of %s",
	"worktree_empty":    "working tree has no changes",
	"staged_empty":      "staging area is empty",
	"log_empty":         "no commits in %s..HEAD",
	"no_tag":            "no start ref given and the most recent tag could not be resolved: %v",
	"select_file_first": "select a file in the file list first",
	"file_empty":        "file is empty: %s",

	"notify_subtitle":  "Commit message copied to clipboard — paste it into the commit box with Cmd+V",
	"notify_pr_title":  "PR title copied to clipboard",
	"notify_stash":     "stash message copied to clipboard",
	"notify_explain":   "file explanation copied to clipboard",
	"notify_split":     "commit split suggestion copied to clipboard",
	"notify_changelog": "changelog copied to clipboard",

	"review_ok":             "OK — no issues found",
	"review_fix_label":      "> Suggested fix: ",
	"review_summary":        "high %d, medium %d, low %d, unclassified %d",
	"review_subtitle_done":  "Review done",
	"review_subtitle_file":  "Review done, report written to file",
	"review_report_notice":  "Many findings; the full report was written to %s\n\n%s",
	"review_report_title":   "# stai review report\n\n",
	"review_blocked_commit": "stai: pre-commit review found high-severity issues, commit blocked (bypass: STAI_DISABLE=1 or git commit --no-verify):",
	"review_blocked_push":   "stai: pre-push review found high-severity issues, push blocked (bypass: STAI_DISABLE=1 or git push --no-verify):",
	"review_failed_commit":  "review failed, committing as-is: %v",
	"review_failed_push":    "branch review failed, pushing as-is: %v",
	"hook_msg_gen_failed":   "commit message generation failed, committing as-is: %v",

	"install_hook_installed":  "hook installed: %s",
	"install_hook_removed":    "hook removed: %s",
	"install_hook_left_alone": "hook left alone (not written by stai): %s",
	"install_removing_failed": "removing hook: %v",
	"install_add_manually":    "Add them manually: SourceTree → Settings → Custom Actions → Add:",
	"install_actions_failed":  "SourceTree custom actions not registered: %v\n",
	"install_caption":         "  Menu caption:  %s",
	"install_script":          "  Script to run:  %s",
	"install_params":          "  Parameters:    %s",
	"install_registered":      "SourceTree custom actions registered: %s (restart SourceTree)",
	"install_remove_manually": "Remove them manually: SourceTree → Settings → Custom Actions:",
	"install_remove_failed":   "SourceTree custom actions not removed: %v\n",
	"install_removed":         "SourceTree custom actions removed: %d (restart SourceTree)",

	"dialog_edit_body":   "Editable — confirm to copy to the clipboard, then paste into the SourceTree commit box",
	"dialog_edit_title":  "stai — edit commit message",
	"dialog_ok":          "OK",
	"dialog_cancel":      "Cancel",
	"mergetool_not_impl": "stai %s: not implemented yet\n  plan: %s\n",
	"lang_fallback":      "stai: unsupported lang %q, falling back to zh-CN (supported: zh-CN, en)",

	// ai package user-facing errors
	"err_commit_invalid": "model output still violates the rules",
	"err_pr_invalid":     "PR title/description still violates the rules",
	"err_empty_stash":    "model returned an empty stash message",
	"err_empty_pr_title": "model returned an empty PR title",

	// install wizard
	"wizard_welcome":          "Welcome to stai! No config found; starting guided setup.",
	"wizard_choose_lang":      "Choose language / 选择语言:",
	"wizard_lang_zh":          "中文",
	"wizard_lang_en":          "English",
	"wizard_base_url":         "Provider base URL",
	"wizard_api_key":          "Provider API key",
	"wizard_model_loading":    "Fetching model list...",
	"wizard_model_list_empty": "No models returned; please enter the model name manually.",
	"wizard_model_prompt":     "Choose a model",
	"wizard_model_manual":     "Could not fetch the model list; please enter the model name manually",
	"wizard_actions_prompt":   "Select SourceTree custom actions to register (enter numbers separated by commas; press Enter for all; 0 for none)",
	"wizard_actions_all":      "All SourceTree custom actions will be registered",
	"wizard_actions_none":     "Skipping SourceTree custom actions",
	"wizard_actions_done":     "Selected %d SourceTree custom actions",
	"wizard_config_written":   "Config written to: %s",
	"wizard_invalid_choice":   "Invalid choice; please try again",

	// uninstall
	"uninstall_config_prompt":  "Remove the global config file %s?",
	"uninstall_config_removed": "Config file removed: %s",
	"uninstall_config_failed":  "Failed to remove config file: %v",
	"uninstall_log_failed":     "Failed to remove log file: %v",
	"uninstall_done":           "Uninstall complete",
}

var messages = map[string]map[string]string{
	"zh-CN": zhCN,
	"en":    en,
}
