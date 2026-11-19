import { describe, it, expect, vi } from 'vitest'
import { fireEvent, render, screen } from '@testing-library/react'
import { ModelInput } from './model-input'

const models = ['glm-4', 'gpt-5']

function renderInput(value: string) {
  const onChange = vi.fn()
  const onAdd = vi.fn()
  const utils = render(
    <ModelInput value={value} onChange={onChange} models={models} onAdd={onAdd} />
  )
  return { onChange, onAdd, ...utils }
}

describe('ModelInput', () => {
  it('offers the library names in a datalist', () => {
    const { container } = renderInput('')

    const options = Array.from(
      container.querySelectorAll('datalist option')
    ).map((o) => o.getAttribute('value'))
    expect(options).toEqual(models)
  })

  it('passes typed text through unchanged', () => {
    const { onChange } = renderInput('')

    fireEvent.change(screen.getByRole('combobox'), {
      target: { value: 'my-model' },
    })

    expect(onChange).toHaveBeenCalledWith('my-model')
  })

  it('offers to add a name that is missing from the library', () => {
    const { onAdd } = renderInput('  brand-new  ')

    fireEvent.click(screen.getByRole('button', { name: '加入模型库' }))

    expect(onAdd).toHaveBeenCalledWith('brand-new')
  })

  it('does not offer adding a library name or an empty value', () => {
    renderInput('glm-4')
    expect(
      screen.queryByRole('button', { name: '加入模型库' })
    ).not.toBeInTheDocument()

    renderInput('')
    expect(
      screen.queryByRole('button', { name: '加入模型库' })
    ).not.toBeInTheDocument()
  })
})
