//
//  UIViewController+Delay.swift
//  Sense
//
//  Created by Jimmy Lu on 12/14/16.
//  Copyright © 2016 Hello. All rights reserved.
//

import Foundation

extension UIViewController {

    // (Void) -> Void as a parameter list stopped meaning "no arguments" in
    // Swift 4; it is spelled () -> Void now.
    @objc func dismiss(delay: Double, animated: Bool, completion: (() -> Void)?) {
        DispatchQueue.main.asyncAfter(deadline: .now() + delay, execute: {[weak self] in
            self?.dismiss(animated: animated, completion: completion)
        })
    }
    
}
