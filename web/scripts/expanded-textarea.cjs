async function editorFor(control){
 const page=control.page()
 const wrapper=control.locator('xpath=ancestor-or-self::*[@data-expanded-textarea][1]')
 const trigger=wrapper.locator('[data-expanded-textarea-trigger]')
 const id=await trigger.getAttribute('id')
 if(await trigger.getAttribute('aria-expanded')!=='true')await trigger.click()
 const editor=page.locator('[id='+JSON.stringify(id+'-editor')+']')
 await editor.waitFor({state:'visible'})
 return editor
}
async function fillExpanded(control,value){
 const editor=await editorFor(control)
 await editor.fill(value)
 await control.page().locator('.expanded-textarea-panel:visible .expanded-textarea-actions button').click()
 await editor.waitFor({state:'detached'})
}
module.exports={editorFor,fillExpanded}
