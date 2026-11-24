import { useCallback, useEffect, useState } from 'react'
import type { FileEntry } from '../../api/sources'

export function useFileSelection(entries: FileEntry[], resetKey: string) {
  const [selectedNames, setSelectedNames] = useState<Set<string>>(new Set())

  useEffect(() => {
    setSelectedNames(new Set())
  }, [resetKey])

  const selectableEntries = entries.filter((entry) => entry.type !== 'unsupported')
  const selectedEntries = entries.filter((entry) => selectedNames.has(entry.name) && entry.type !== 'unsupported')
  const allVisibleSelected = selectableEntries.length > 0 && selectableEntries.every((entry) => selectedNames.has(entry.name))

  const toggleSelected = useCallback((name: string, selected: boolean) => {
    setSelectedNames((current) => {
      const next = new Set(current)
      if (selected) next.add(name)
      else next.delete(name)
      return next
    })
  }, [])

  const toggleAllSelected = useCallback((selected: boolean) => {
    setSelectedNames((current) => {
      const next = new Set(current)
      for (const entry of selectableEntries) {
        if (selected) next.add(entry.name)
        else next.delete(entry.name)
      }
      return next
    })
  }, [selectableEntries])

  const clearSelection = useCallback(() => {
    setSelectedNames(new Set())
  }, [])

  return {
    selectedNames,
    setSelectedNames,
    selectableEntries,
    selectedEntries,
    allVisibleSelected,
    toggleSelected,
    toggleAllSelected,
    clearSelection,
  }
}
