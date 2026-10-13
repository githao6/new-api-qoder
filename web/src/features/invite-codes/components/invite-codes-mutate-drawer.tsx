/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { zodResolver } from '@hookform/resolvers/zod'
import { type FormEvent, useEffect, useState } from 'react'
import { useForm } from 'react-hook-form'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { DateTimePicker } from '@/components/datetime-picker'
import {
  SideDrawerSection,
  sideDrawerContentClassName,
  sideDrawerFooterClassName,
  sideDrawerFormClassName,
  sideDrawerHeaderClassName,
} from '@/components/drawer-layout'
import { Button } from '@/components/ui/button'
import {
  Form,
  FormControl,
  FormDescription,
  FormField,
  FormItem,
  FormLabel,
  FormMessage,
} from '@/components/ui/form'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetClose,
  SheetContent,
  SheetDescription,
  SheetFooter,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { handleServerError } from '@/lib/handle-server-error'
import { addTimeToDate } from '@/lib/time'

import { createInviteCode, updateInviteCode, getInviteCode } from '../api'
import { SUCCESS_MESSAGES } from '../constants'
import {
  getInviteCodeFormSchema,
  type InviteCodeFormValues,
  INVITE_CODE_FORM_DEFAULT_VALUES,
  transformFormDataToPayload,
  transformInviteCodeToFormDefaults,
} from '../lib'
import type { InviteCode } from '../types'
import { useInviteCodes } from './invite-codes-provider'

type InviteCodesMutateDrawerProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  currentRow?: InviteCode
}

export function InviteCodesMutateDrawer({
  open,
  onOpenChange,
  currentRow,
}: InviteCodesMutateDrawerProps) {
  const { t } = useTranslation()
  const isUpdate = !!currentRow
  const inviteCodeId = currentRow?.id
  const { triggerRefresh } = useInviteCodes()
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [inviteCodeLoadState, setInviteCodeLoadState] = useState<
    'idle' | 'loading' | 'ready' | 'error'
  >('idle')
  const [loadedInviteCode, setLoadedInviteCode] = useState<InviteCode | null>(
    null
  )

  const form = useForm<InviteCodeFormValues>({
    resolver: zodResolver(getInviteCodeFormSchema(t)),
    defaultValues: INVITE_CODE_FORM_DEFAULT_VALUES,
  })

  // Load existing data when updating
  useEffect(() => {
    if (!open) {
      setInviteCodeLoadState('idle')
      setLoadedInviteCode(null)
      return
    }

    if (!isUpdate || inviteCodeId === undefined) {
      form.reset(INVITE_CODE_FORM_DEFAULT_VALUES)
      setInviteCodeLoadState('ready')
      setLoadedInviteCode(null)
      return
    }

    let ignoreResult = false

    form.reset(INVITE_CODE_FORM_DEFAULT_VALUES)
    setInviteCodeLoadState('loading')
    setLoadedInviteCode(null)

    void getInviteCode(inviteCodeId)
      .then((result) => {
        if (ignoreResult) return

        if (
          !result.success ||
          !result.data ||
          result.data.id !== inviteCodeId
        ) {
          setInviteCodeLoadState('error')
          handleServerError(result, t('Failed to load'))
          return
        }

        form.reset(transformInviteCodeToFormDefaults(result.data))
        setLoadedInviteCode(result.data)
        setInviteCodeLoadState('ready')
      })
      .catch((error: unknown) => {
        if (ignoreResult) return

        setInviteCodeLoadState('error')
        handleServerError(error)
      })

    return () => {
      ignoreResult = true
    }
  }, [open, isUpdate, inviteCodeId, form, t])

  const isUpdateReady =
    !isUpdate ||
    (inviteCodeLoadState === 'ready' && loadedInviteCode?.id === inviteCodeId)
  const isLoadingInviteCode = inviteCodeLoadState === 'loading'

  const onSubmit = async (data: InviteCodeFormValues) => {
    if (isUpdate && (!currentRow || !loadedInviteCode || !isUpdateReady)) {
      return
    }

    setIsSubmitting(true)
    try {
      const basePayload = transformFormDataToPayload(data)

      if (isUpdate && currentRow && loadedInviteCode) {
        const result = await updateInviteCode({
          ...basePayload,
          id: currentRow.id,
        })
        if (result.success) {
          toast.success(t(SUCCESS_MESSAGES.INVITE_CODE_UPDATED))
          onOpenChange(false)
          triggerRefresh()
        } else {
          handleServerError(result)
        }
      } else {
        // Create mode
        const result = await createInviteCode(basePayload)
        if (result.success) {
          const count = result.data?.length || 0
          toast.success(
            count > 1
              ? t('Successfully created {{count}} invite codes', {
                  count,
                })
              : t(SUCCESS_MESSAGES.INVITE_CODE_CREATED)
          )
          onOpenChange(false)
          triggerRefresh()
        } else {
          handleServerError(result)
        }
      }
    } catch (error) {
      handleServerError(error)
    } finally {
      setIsSubmitting(false)
    }
  }

  const handleSubmit = (event: FormEvent<HTMLFormElement>) => {
    void form.handleSubmit(onSubmit)(event)
  }

  const handleSetExpiry = (months: number, days: number, hours: number) => {
    const newDate = addTimeToDate(months, days, hours)
    form.setValue('expired_time', newDate)
  }

  let submitButtonLabel = t('Save changes')
  if (isLoadingInviteCode) {
    submitButtonLabel = t('Loading...')
  } else if (isSubmitting) {
    submitButtonLabel = t('Saving...')
  }

  return (
    <Sheet
      open={open}
      onOpenChange={(v) => {
        onOpenChange(v)
        if (!v) {
          form.reset()
        }
      }}
    >
      <SheetContent className={sideDrawerContentClassName('sm:max-w-[600px]')}>
        <SheetHeader className={sideDrawerHeaderClassName()}>
          <SheetTitle>
            {isUpdate ? t('Update Invite Code') : t('Create Invite Code')}
          </SheetTitle>
          <SheetDescription>
            {isUpdate
              ? t('Update the invite code by providing necessary info.')
              : t(
                  'Add new invite code(s) by providing necessary info. Each code can only be used once for registration.'
                )}{' '}
            {t('Click save when you&apos;re done.')}
          </SheetDescription>
        </SheetHeader>
        <Form {...form}>
          <form
            id='invite-code-form'
            onSubmit={handleSubmit}
            className={sideDrawerFormClassName()}
            aria-busy={isLoadingInviteCode}
          >
            <fieldset
              disabled={!isUpdateReady || isSubmitting}
              className='contents'
            >
              <SideDrawerSection>
                <FormField
                  control={form.control}
                  name='name'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Name')}</FormLabel>
                      <FormControl>
                        <Input {...field} placeholder={t('Enter a name')} />
                      </FormControl>
                      <FormDescription>
                        {t('Name for this invite code (1-20 characters)')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                <FormField
                  control={form.control}
                  name='expired_time'
                  render={({ field }) => (
                    <FormItem>
                      <FormLabel>{t('Expiration Time')}</FormLabel>
                      <div className='flex flex-col gap-2'>
                        <FormControl>
                          <DateTimePicker
                            value={field.value}
                            onChange={field.onChange}
                            placeholder={t('Never expires')}
                          />
                        </FormControl>
                        <div className='grid grid-cols-4 gap-1.5 sm:flex sm:gap-2'>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() => handleSetExpiry(0, 0, 0)}
                          >
                            {t('Never')}
                          </Button>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() => handleSetExpiry(1, 0, 0)}
                          >
                            {t('1M')}
                          </Button>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() => handleSetExpiry(0, 7, 0)}
                          >
                            {t('1W')}
                          </Button>
                          <Button
                            type='button'
                            variant='outline'
                            size='sm'
                            onClick={() => handleSetExpiry(0, 1, 0)}
                          >
                            {t('1 Day')}
                          </Button>
                        </div>
                      </div>
                      <FormDescription>
                        {t('Leave empty for never expires')}
                      </FormDescription>
                      <FormMessage />
                    </FormItem>
                  )}
                />

                {!isUpdate && (
                  <FormField
                    control={form.control}
                    name='count'
                    render={({ field }) => (
                      <FormItem>
                        <FormLabel>{t('Quantity')}</FormLabel>
                        <FormControl>
                          <Input
                            {...field}
                            type='number'
                            min='1'
                            max='100'
                            placeholder={t('Number of codes to create')}
                            onChange={(e) =>
                              field.onChange(
                                Number.parseInt(e.target.value, 10) || 1
                              )
                            }
                          />
                        </FormControl>
                        <FormDescription>
                          {t(
                            'Create multiple one-time-use invite codes at once (1-100)'
                          )}
                        </FormDescription>
                        <FormMessage />
                      </FormItem>
                    )}
                  />
                )}
              </SideDrawerSection>
            </fieldset>
          </form>
        </Form>
        <SheetFooter className={sideDrawerFooterClassName()}>
          <SheetClose render={<Button variant='outline' />}>
            {t('Close')}
          </SheetClose>
          <Button
            form='invite-code-form'
            type='submit'
            disabled={isSubmitting || !isUpdateReady}
          >
            {submitButtonLabel}
          </Button>
        </SheetFooter>
      </SheetContent>
    </Sheet>
  )
}
