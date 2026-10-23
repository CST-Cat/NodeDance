// Exercise the actual installed Radix/UIArc controls rather than hidden form inputs.
async function selectValue(control,value){
 const page=control.page();await control.click();const option=page.locator('[role=menuitemradio][data-option-value='+JSON.stringify(String(value))+']');await option.click();await option.waitFor({state:"detached"});
}
async function selectedValue(control){return control.getAttribute('data-selected-value')}
async function readOptions(control){await control.click();const options=await control.page().locator('[role=menuitemradio][data-option-value]').evaluateAll(items=>items.map(el=>({value:el.getAttribute('data-option-value'),label:el.textContent})));await control.page().keyboard.press('Escape');return options}
module.exports={selectValue,selectedValue,readOptions}
