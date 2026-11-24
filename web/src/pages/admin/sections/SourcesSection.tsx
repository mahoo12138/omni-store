import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { mergeClasses, Table, TableBody, TableCell, TableHeader, TableHeaderCell, TableRow } from '@fluentui/react-components'
import { ApiRequestError } from '../../../api/client'
import { adminDeleteSource, adminListSources, adminSetSourceDisabled, type AdminSource } from '../../../api/admin'
import { Badge } from '../../../components/ui/Badge'
import { Button } from '../../../components/ui/Button'
import { DialogWrap } from '../../../components/ui/Dialog'
import { IconAction } from '../../../components/ui/IconAction'
import { StateView } from '../../../components/shell/StateView'
import { IconPlus, IconServer, IconSettings, IconTrash } from '../../../components/ui/Icon'
import { formatBytes } from '../../../utils/format'
import { useStyles } from './sources.styles'
import { HorizontalDataRegion } from '../shared'
import { SectionCard } from '../SectionCard'
import { CreateSourceDialog, EditSourceDialog } from '../dialogs/SourceDialogs'

export function SourcesSection() {
  const css = useStyles()
  const queryClient = useQueryClient()
  const sources = useQuery({ queryKey: ['admin-sources'], queryFn: adminListSources })
  const [createOpen, setCreateOpen] = useState(false)
  const [editId, setEditId] = useState<string | null>(null)
  const [deleting, setDeleting] = useState<AdminSource | null>(null)

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['admin-sources'] })

  const disableMut = useMutation({
    mutationFn: ({ id, disabled }: { id: string; disabled: boolean }) =>
      adminSetSourceDisabled(id, disabled),
    onSuccess: refresh, onError: (err) => alert(err instanceof ApiRequestError ? err.message : '操作失败'),
  })
  const deleteMut = useMutation({
    mutationFn: adminDeleteSource, onSuccess: () => { setDeleting(null); refresh() },
    onError: (err) => alert(err instanceof ApiRequestError ? err.message : '删除失败'),
  })

  return (
    <>
      <SectionCard
        icon={<IconServer size={18} />}
        tone="brand"
        hint={<>共 {sources.data?.length ?? 0} 个</>}
        busy={sources.isFetching}
        actions={<Button onClick={() => setCreateOpen(true)} icon={<IconPlus size={14} />}>新建存储源</Button>}
        bodyPadding="none"
      >
        <HorizontalDataRegion name="存储源" busy={sources.isPending}>
          <Table size="small" className={mergeClasses(css.compactTable, css.compactTableWide)}>
            <TableHeader>
              <TableRow>
                <TableHeaderCell className={css.compactTh}>名称</TableHeaderCell>
                <TableHeaderCell className={css.compactTh}>状态</TableHeaderCell>
                <TableHeaderCell className={css.compactTh}>配额</TableHeaderCell>
                <TableHeaderCell className={css.compactTh}>访问协议</TableHeaderCell>
                <TableHeaderCell className={css.compactTh}>操作</TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {sources.data?.map((s) => {
                const protocols = [
                  s.webdav_enabled ? 'WebDAV' : null,
                  s.s3_enabled ? 'S3' : null,
                  s.static_enabled ? '静态 HTTP' : null,
                ].filter((protocol): protocol is string => Boolean(protocol))
                return (
                  <TableRow key={s.key} className={css.compactTr}>
                    <TableCell className={mergeClasses(css.compactTd, css.sourceIdentityCell)}>
                      <div className={css.sourceCellName} title={s.name}>{s.name}</div>
                      <code className={css.sourceCellPath} title={s.root_path}>{s.root_path}</code>
                    </TableCell>
                    <TableCell className={css.compactTd}>
                      <Badge color={s.is_disabled ? 'gray' : 'green'}>{s.is_disabled ? '已禁用' : '正常'}</Badge>
                    </TableCell>
                    <TableCell className={css.compactTd}>
                      {s.quota_bytes > 0 ? formatBytes(s.quota_bytes) : '不限'}
                    </TableCell>
                    <TableCell className={css.compactTd}>
                      <div className={css.sourceProtocols}>
                        {protocols.length === 0 ? (
                          <span className={css.sourceCellMuted}>均未启用</span>
                        ) : protocols.map((protocol) => (
                          <Badge key={protocol} color="gray">{protocol}</Badge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell className={css.compactTd}>
                      <div className={css.sourceActionRow}>
                        <IconAction
                          label={`配置存储源 ${s.name}`}
                          tooltip="配置"
                          icon={<IconSettings size={16} />}
                          onClick={() => setEditId(s.key)}
                        />
                        <Button
                          variant="ghost"
                          style={{ minWidth: 0 }}
                          onClick={() => disableMut.mutate({ id: s.key, disabled: !s.is_disabled })}
                          disabled={disableMut.isPending}
                          aria-label={`${s.is_disabled ? '启用' : '禁用'}存储源 ${s.name}`}
                        >
                          {s.is_disabled ? '启用' : '禁用'}
                        </Button>
                        <IconAction
                          label={`删除存储源 ${s.name}`}
                          tooltip="删除"
                          icon={<IconTrash size={16} />}
                          danger
                          onClick={() => setDeleting(s)}
                        />
                      </div>
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
          {sources.isPending ? <StateView layout="plain" kind="pending" title="正在加载存储源…" /> : null}
          {sources.isError ? (
            <StateView
              layout="plain"
              kind="error"
              title="无法加载存储源"
              description="请检查服务状态后重试。"
              action={<Button variant="secondary" onClick={() => sources.refetch()}>重新加载</Button>}
            />
          ) : null}
          {sources.isSuccess && sources.data.length === 0 ? (
            <StateView layout="plain" kind="empty" title="还没有存储源" />
          ) : null}
        </HorizontalDataRegion>
      </SectionCard>

      {/* 新建存储源 弹窗 */}
      <CreateSourceDialog open={createOpen} onOpenChange={setCreateOpen} />

      {/* 编辑存储源 弹窗 */}
      {editId && (
        <EditSourceDialog
          sourceKey={editId}
          onClose={() => setEditId(null)}
        />
      )}

      {/* 删除确认 弹窗 */}
      <DialogWrap
        open={!!deleting}
        onOpenChange={(o) => { if (!o) setDeleting(null) }}
        title="删除存储源"
        description={`确定要删除「${deleting?.name ?? ''}」吗？`}
        footer={
          <>
            <Button variant="ghost" onClick={() => setDeleting(null)}>
              取消
            </Button>
            <Button
              variant="danger"
              onClick={() => deleting && deleteMut.mutate(deleting.key)}
              disabled={deleteMut.isPending}
            >
              {deleteMut.isPending ? '删除中…' : '确认删除'}
            </Button>
          </>
        }
      >
        <p style={{ margin: 0, fontSize: '13px', color: 'var(--colorNeutralForeground1)' }}>
          此操作只会从 OmniStore 中移除该存储源及其授权关系，不会删除磁盘上的真实文件。该操作不可撤销。
        </p>
      </DialogWrap>
    </>
  )
}
